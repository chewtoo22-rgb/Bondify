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

                val handshaked = builder.handshake()
                if (stopping) {
                    handshaked.close()
                    return@Thread
                }
                if (handshaked.pathErrors.isNotEmpty()) {
                    Log.w(TAG, "some paths failed to join: ${handshaked.pathErrors}")
                }

                acquired.activateRuntime(handshaked)
                checkNotStopping()

                val handshakeMtu = handshaked.getMTU()
                val mtu = if (handshakeMtu > 0) handshakeMtu.toInt() else 1280
                val currentNetworks = acquired.currentNetworks()
                val vpnBuilder = Builder()
                    .setSession(getString(R.string.app_name))
                    .setMtu(mtu)
                    .addAddress(handshaked.tunnelIP, handshaked.prefix.toInt())
                    .addRoute("0.0.0.0", 0)
                    .addDnsServer("1.1.1.1")
                    .setUnderlyingNetworks(currentNetworks.toTypedArray())

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
                status = TunnelStatus.Connected(handshaked.tunnelIP, currentNetworks.size)
                Log.i(
                    TAG,
                    "tunnel established: session=${handshaked.sessionIndexHex} ip=${handshaked.tunnelIP} " +
                        "prefix=${handshaked.prefix} gw=${handshaked.gatewayIP} mtu=$mtu paths=${currentNetworks.size}",
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
        val currentNetworks: () -> List<Network>,
        val activateRuntime: (Tunnel) -> Unit,
    )

    private fun acquirePaths(builder: TunnelBuilder, endpoint: RelayEndpoint): AcquiredPaths {
        val lock = Object()
        var gathering = true
        var runtimeTunnel: Tunnel? = null
        val desiredNetworks = ActivePathRegistry<Network>()
        val installedNetworks = mutableMapOf<String, Network>()

        fun dialSocketFd(network: Network, label: String): Long {
            var detachedFd: Int? = null
            val socket = DatagramSocket()
            try {
                network.bindSocket(socket)
                check(protect(socket)) {
                    "VpnService.protect() rejected the $label uplink socket"
                }
                socket.connect(InetSocketAddress(endpoint.host, endpoint.port))
                val pfd = ParcelFileDescriptor.fromDatagramSocket(socket)
                detachedFd = pfd.detachFd()
                val fd = checkNotNull(detachedFd)
                detachedFd = null
                return fd.toLong()
            } finally {
                socket.close()
                detachedFd?.let { fd -> runCatching { ParcelFileDescriptor.adoptFd(fd).close() } }
            }
        }

        fun syncRuntimePaths() {
            synchronized(lock) {
                val t = runtimeTunnel ?: return
                val desired = desiredNetworks.snapshot()

                for ((label, installedNetwork) in installedNetworks.toMap()) {
                    if (desired[label] == installedNetwork) {
                        continue
                    }
                    try {
                        t.dropPathLabel(label)
                        installedNetworks.remove(label)
                        Log.i(TAG, "runtime-dropped path: $label")
                    } catch (e: Exception) {
                        Log.w(TAG, "could not runtime-drop $label path: ${e.message}")
                        continue
                    }
                }

                for ((label, desiredNetwork) in desired) {
                    if (installedNetworks[label] == desiredNetwork) {
                        continue
                    }
                    try {
                        t.addPathFD(dialSocketFd(desiredNetwork, label), label)
                        installedNetworks[label] = desiredNetwork
                        Log.i(TAG, "runtime-added path: $label")
                    } catch (e: Exception) {
                        Log.w(TAG, "could not runtime-add $label path: ${e.message}")
                    }
                }
            }
        }

        fun onNetworkAvailable(network: Network, label: String) {
            val previous = desiredNetworks.replace(label, network)
            if (previous != network) {
                Log.i(TAG, "physical path available: $label")
            }
            val shouldSync = synchronized(lock) { !gathering }
            if (shouldSync) {
                syncRuntimePaths()
            }
        }

        fun onNetworkLost(network: Network, label: String) {
            if (!desiredNetworks.removeIfCurrent(label, network)) {
                Log.i(TAG, "ignoring stale network loss for replaced path: $label")
                return
            }
            Log.i(TAG, "physical path lost: $label")
            val shouldSync = synchronized(lock) { !gathering }
            if (shouldSync) {
                syncRuntimePaths()
            }
        }

        val wifiRequest = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        wifiCallback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = onNetworkAvailable(network, "wifi")
            override fun onLost(network: Network) = onNetworkLost(network, "wifi")
        }
        connectivityManager.requestNetwork(wifiRequest, wifiCallback!!)

        val cellularRequest = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        cellularCallback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = onNetworkAvailable(network, "cellular")
            override fun onLost(network: Network) = onNetworkLost(network, "cellular")
        }
        connectivityManager.requestNetwork(cellularRequest, cellularCallback!!)

        Thread.sleep(PATH_WAIT_MS)

        val initialNetworks = desiredNetworks.snapshot()
        var added = 0
        synchronized(lock) {
            for ((label, network) in initialNetworks) {
                if (stopping) {
                    break
                }
                try {
                    builder.addPathFD(dialSocketFd(network, label), label)
                    installedNetworks[label] = network
                    added++
                    Log.i(TAG, "added initial path: $label")
                } catch (e: Exception) {
                    Log.w(TAG, "could not add initial $label path: ${e.message}")
                }
            }
            gathering = false
        }

        return AcquiredPaths(
            count = added,
            currentNetworks = { desiredNetworks.snapshot().values.toList() },
            activateRuntime = { t ->
                synchronized(lock) {
                    runtimeTunnel = t
                }
                syncRuntimePaths()
            },
        )
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
    return RelayEndpoint(uri.host.removePrefix("[").removeSuffix("]"), uri.port)
}

/** Preference names plus Keystore-backed access to the client identity. */
object Prefs {
    const val NAME = "bondify_prefs"
    const val KEY_RELAY_ADDR = "relay_addr"
    const val KEY_RELAY_PUBKEY = "relay_pubkey"

    fun clientKey(prefs: SharedPreferences): String? = ClientKeyStore.getOrMigrate(prefs)

    fun setClientKey(prefs: SharedPreferences, value: String) {
        ClientKeyStore.store(prefs, value)
    }
}
