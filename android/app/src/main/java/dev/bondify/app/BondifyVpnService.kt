package dev.bondify.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import java.net.DatagramSocket
import java.net.InetSocketAddress
import java.net.URI
import mobile.Tunnel
import mobile.TunnelBuilder

internal data class RelayEndpoint(val host: String, val port: Int)

/**
 * Bondify's Android VpnService. One instance owns the whole tunnel lifecycle: acquiring a
 * socket per physical uplink (Wi-Fi, cellular), establishing the TUN interface, handing both
 * to the Go core via [mobile.TunnelBuilder]/[mobile.Tunnel], and keeping a foreground
 * notification alive. The app also asks the user for a battery-optimization exemption; the
 * actual screen-off survival gate still requires a real device (see ARCHITECTURE.md §9).
 *
 * Path acquisition, not TUN setup, is the actual hard part on Android: unlike the Linux CLI
 * client (core/tun/linux.go's DialUDPViaDevice, using SO_BINDTODEVICE), an app has no
 * privilege to choose which physical network a socket egresses on -- that's
 * [ConnectivityManager.Network.bindSocket], callable only from here, not from Go. So this
 * class does the network-selection and socket plumbing, and [mobile.TunnelBuilder.addPathFD]
 * just adopts whatever already-bound, already-protected fd it's handed.
 */
class BondifyVpnService : VpnService() {

    companion object {
        private const val TAG = "BondifyVpnService"
        const val ACTION_CONNECT = "dev.bondify.app.CONNECT"
        const val ACTION_DISCONNECT = "dev.bondify.app.DISCONNECT"
        const val EXTRA_RELAY_ADDR = "relay_addr"
        const val EXTRA_RELAY_PUBKEY = "relay_pubkey"

        private const val NOTIFICATION_CHANNEL_ID = "bondify_tunnel"
        private const val NOTIFICATION_ID = 1

        // How long to wait for both Wi-Fi and cellular to report available before starting
        // with whatever showed up -- long enough for a cold radio to associate, short enough
        // not to stall a single-uplink device (e.g. a Wi-Fi-only tablet) waiting on a
        // cellular network that will never come.
        private const val PATH_WAIT_MS = 4000L

        @Volatile
        var status: TunnelStatus = TunnelStatus.Disconnected
            private set
    }

    sealed class TunnelStatus {
        object Disconnected : TunnelStatus()
        object Connecting : TunnelStatus()
        data class Connected(val tunnelIp: String, val pathCount: Int) : TunnelStatus()
        data class Failed(val message: String) : TunnelStatus()
    }

    private var tunnel: Tunnel? = null
    private var tunFd: ParcelFileDescriptor? = null
    private val connectivityManager by lazy {
        getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
    }
    private var wifiCallback: ConnectivityManager.NetworkCallback? = null
    private var cellularCallback: ConnectivityManager.NetworkCallback? = null
    @Volatile
    private var stopping = false
    private var connectThread: Thread? = null
    private var runThread: Thread? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_DISCONNECT -> {
                disconnect()
                return START_NOT_STICKY
            }
            ACTION_CONNECT -> {
                if (status is TunnelStatus.Connecting || status is TunnelStatus.Connected) {
                    Log.i(TAG, "ignoring duplicate CONNECT while tunnel is already active")
                    return START_NOT_STICKY
                }
                val relayAddr = intent.getStringExtra(EXTRA_RELAY_ADDR)
                val relayPubKey = intent.getStringExtra(EXTRA_RELAY_PUBKEY)
                if (relayAddr.isNullOrBlank() || relayPubKey.isNullOrBlank()) {
                    Log.e(TAG, "CONNECT missing relay address/pubkey")
                    stopSelf()
                    return START_NOT_STICKY
                }
                startForegroundWithNotification(connecting = true)
                connect(relayAddr, relayPubKey)
                return START_NOT_STICKY
            }
        }
        return START_NOT_STICKY
    }

    private fun connect(relayAddr: String, relayPubKeyB64: String) {
        stopping = false
        status = TunnelStatus.Connecting
        connectThread = Thread {
            try {
                val prefs = getSharedPreferences(Prefs.NAME, Context.MODE_PRIVATE)
                val clientKeyB64 = Prefs.clientKey(prefs)
                    ?: error("no client key generated yet (MainActivity should have done this on first run)")

                val builder = TunnelBuilder(
                    relayAddr,
                    relayPubKeyB64,
                    clientKeyB64,
                    /* scheduler = */ "hol-aware",
                    /* mode = */ "speed",
                    /* fec = */ true,
                )

                val endpoint = parseRelayEndpoint(relayAddr)
                val acquired = acquirePaths(builder, endpoint)
                checkNotStopping()
                if (acquired.count == 0) {
                    error("no uplink (Wi-Fi or cellular) became available within ${PATH_WAIT_MS}ms")
                }

                // Handshake first, TUN interface second: the relay assigns this device's
                // tunnel IP dynamically from its pool (see PROTOCOL.md's cfg_push), so unlike
                // a static WireGuard-style config, the real address isn't known until the
                // handshake completes -- and VpnService.Builder can't be reconfigured after
                // establish(), so it must be built with the *real* value, not a placeholder.
                val handshaked = builder.handshake()
                if (stopping) {
                    handshaked.close()
                    return@Thread
                }
                if (handshaked.pathErrors.isNotEmpty()) {
                    Log.w(TAG, "some paths failed to join: ${handshaked.pathErrors}")
                }

                val handshakeMtu = handshaked.getMTU() // Kotlin's Java-property synthesis doesn't kick in for all-caps getMTU()
                val mtu = if (handshakeMtu > 0) handshakeMtu.toInt() else 1280
                val vpnBuilder = Builder()
                    .setSession(getString(R.string.app_name))
                    .setMtu(mtu)
                    .addAddress(handshaked.tunnelIP, handshaked.prefix.toInt())
                    .addRoute("0.0.0.0", 0)
                    .addDnsServer("1.1.1.1")
                    .setUnderlyingNetworks(acquired.networks.toTypedArray())

                val pfd = vpnBuilder.establish()
                    ?: error("VpnService.Builder.establish() returned null (permission revoked mid-flight?)")
                if (stopping) {
                    pfd.close()
                    handshaked.close()
                    return@Thread
                }
                tunFd = pfd

                handshaked.attachTUN(pfd.fd.toLong())
                tunnel = handshaked
                status = TunnelStatus.Connected(handshaked.tunnelIP, acquired.count)
                Log.i(
                    TAG,
                    "tunnel established: session=${handshaked.sessionIndexHex} ip=${handshaked.tunnelIP} " +
                        "prefix=${handshaked.prefix} gw=${handshaked.gatewayIP} mtu=$mtu paths=${acquired.count}",
                )
                startForegroundWithNotification(connecting = false)

                runThread = Thread {
                    val err = handshaked.awaitExit()
                    if (err.isNotEmpty()) {
                        Log.e(TAG, "tunnel exited with error: $err")
                        disconnect(TunnelStatus.Failed(err))
                    }
                }.also { it.start() }
            } catch (_: InterruptedException) {
                Log.i(TAG, "connection attempt cancelled")
            } catch (e: Exception) {
                if (stopping) {
                    Log.i(TAG, "connection attempt stopped: ${e.message}")
                    return@Thread
                }
                Log.e(TAG, "connect failed", e)
                disconnect(TunnelStatus.Failed(e.message ?: e.toString()))
            }
        }.also {
            it.name = "bondify-connect"
            it.start()
        }
    }

    private data class AcquiredPaths(
        val count: Int,
        val networks: List<Network>,
    )

    /**
     * Requests Wi-Fi and cellular networks, and for each that becomes available within
     * [PATH_WAIT_MS], dials a UDP socket to the relay on it, binds it to that network,
     * protects it from the VPN's own capture, and hands its fd to [builder].
     *
     * The callbacks intentionally remain registered after this function returns so Android
     * keeps the requested physical networks alive. The `accepting` gate prevents a late or
     * repeated callback from mutating [builder] after its handshake has started. Runtime
     * path replacement after `onLost` needs a separate Go `AddPath` API and is tracked in
     * PROJECT_STATUS.md rather than being faked by mutating this one-shot builder.
     */
    private fun acquirePaths(builder: TunnelBuilder, endpoint: RelayEndpoint): AcquiredPaths {
        var added = 0
        val lock = Object()
        var accepting = true
        val addedLabels = mutableSetOf<String>()
        val networks = mutableListOf<Network>()

        fun tryAddPath(network: Network, label: String) {
            synchronized(lock) {
                if (!accepting || stopping || label in addedLabels) {
                    return
                }
                var detachedFd: Int? = null
                try {
                    val socket = DatagramSocket()
                    try {
                        network.bindSocket(socket)
                        check(protect(socket)) {
                            "VpnService.protect() rejected the $label uplink socket"
                        }
                        socket.connect(InetSocketAddress(endpoint.host, endpoint.port))
                        val pfd = ParcelFileDescriptor.fromDatagramSocket(socket)
                        detachedFd = pfd.detachFd()
                        val fdForGo = checkNotNull(detachedFd)
                        detachedFd = null // AddPathFD consumes the descriptor even on error
                        builder.addPathFD(fdForGo.toLong(), label)
                    } finally {
                        socket.close()
                    }
                    addedLabels += label
                    networks += network
                    added++
                    Log.i(TAG, "added path: $label")
                } catch (e: Exception) {
                    detachedFd?.let { fd ->
                        runCatching { ParcelFileDescriptor.adoptFd(fd).close() }
                    }
                    Log.w(TAG, "could not add $label path: ${e.message}")
                }
            }
        }

        val wifiRequest = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        wifiCallback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = tryAddPath(network, "wifi")
        }
        connectivityManager.requestNetwork(wifiRequest, wifiCallback!!)

        val cellularRequest = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        cellularCallback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = tryAddPath(network, "cellular")
        }
        connectivityManager.requestNetwork(cellularRequest, cellularCallback!!)

        Thread.sleep(PATH_WAIT_MS)
        return synchronized(lock) {
            // If a callback is already adding a path, acquiring this lock waits for it to
            // finish. No builder mutation can cross this boundary into Handshake().
            accepting = false
            AcquiredPaths(added, networks.toList())
        }
    }

    private fun checkNotStopping() {
        if (stopping || Thread.currentThread().isInterrupted) {
            throw InterruptedException("connection attempt cancelled")
        }
    }

    private fun disconnect(
        finalStatus: TunnelStatus = TunnelStatus.Disconnected,
        stopService: Boolean = true,
    ) {
        stopping = true
        connectThread?.interrupt()
        connectThread = null
        try {
            tunnel?.close()
        } catch (e: Exception) {
            Log.w(TAG, "error closing tunnel", e)
        }
        tunnel = null
        wifiCallback?.let { runCatching { connectivityManager.unregisterNetworkCallback(it) } }
        cellularCallback?.let { runCatching { connectivityManager.unregisterNetworkCallback(it) } }
        wifiCallback = null
        cellularCallback = null
        try {
            tunFd?.close()
        } catch (e: Exception) {
            Log.w(TAG, "error closing tun fd", e)
        }
        tunFd = null
        status = finalStatus
        stopForeground(STOP_FOREGROUND_REMOVE)
        if (stopService) {
            stopSelf()
        }
    }

    override fun onRevoke() {
        // The system calls this when the user revokes VPN permission from Settings directly
        // (not through our own UI) -- must still tear down cleanly, not just die.
        disconnect()
        super.onRevoke()
    }

    override fun onDestroy() {
        val finalStatus = status.takeIf { it is TunnelStatus.Failed } ?: TunnelStatus.Disconnected
        disconnect(finalStatus, stopService = false)
        super.onDestroy()
    }

    private fun startForegroundWithNotification(connecting: Boolean) {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                NOTIFICATION_CHANNEL_ID,
                getString(R.string.notification_channel_name),
                NotificationManager.IMPORTANCE_LOW,
            )
            nm.createNotificationChannel(channel)
        }

        val openApp = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE,
        )
        val text = if (connecting) getString(R.string.notification_connecting) else getString(R.string.notification_connected)
        val notification: Notification = Notification.Builder(this, NOTIFICATION_CHANNEL_ID)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setContentIntent(openApp)
            .setOngoing(true)
            .build()

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(NOTIFICATION_ID, notification, android.content.pm.ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }
}

internal fun parseRelayEndpoint(addr: String): RelayEndpoint {
    val uri = runCatching { URI("udp://$addr") }
        .getOrElse { throw IllegalArgumentException("invalid relay address \"$addr\"", it) }
    require(!uri.host.isNullOrBlank() && uri.port in 1..65535 && uri.rawUserInfo == null) {
        "relay address must be host:port or [IPv6]:port, got \"$addr\""
    }
    require(uri.path.isNullOrEmpty() && uri.query == null && uri.fragment == null) {
        "relay address cannot contain a path, query, or fragment"
    }
    // java.net.URI retains square brackets around IPv6 literals on some Android/JDK
    // versions; InetSocketAddress expects the address itself.
    return RelayEndpoint(uri.host.removePrefix("[").removeSuffix("]"), uri.port)
}

/** Tiny local-only preference wrapper -- see MainActivity for where the client key is generated. */
object Prefs {
    const val NAME = "bondify_prefs"
    const val KEY_RELAY_ADDR = "relay_addr"
    const val KEY_RELAY_PUBKEY = "relay_pubkey"
    private const val KEY_CLIENT_KEY = "client_key_b64"

    fun clientKey(prefs: SharedPreferences): String? = prefs.getString(KEY_CLIENT_KEY, null)

    fun setClientKey(prefs: SharedPreferences, value: String) {
        prefs.edit().putString(KEY_CLIENT_KEY, value).apply()
    }
}
