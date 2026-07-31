package dev.bondify.app

import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import mobile.Mobile

/**
 * Onboarding + connect/disconnect UI. Deliberately minimal for phase 5's scope (see
 * ARCHITECTURE.md §5's gate table): generate/display this device's key on first run, take a
 * relay address + public key, start/stop [BondifyVpnService]. The traffic-intelligence
 * dashboard, PairBond flows, etc. are later phases (7/8).
 */
class MainActivity : AppCompatActivity() {

    private var relayAddrInput: EditText? = null
    private var relayPubKeyInput: EditText? = null
    private lateinit var connectButton: Button
    private lateinit var statusText: TextView
    private var myKeyText: TextView? = null

    private val vpnPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result ->
        if (result.resultCode == RESULT_OK) {
            startTunnel()
        } else {
            Toast.makeText(this, "VPN permission denied", Toast.LENGTH_SHORT).show()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val compact = CoverScreen.isCompactWidth(resources.configuration.screenWidthDp)
        setContentView(if (compact) R.layout.activity_main_compact else R.layout.activity_main)

        relayAddrInput = findViewById(R.id.relayAddrInput)
        relayPubKeyInput = findViewById(R.id.relayPubKeyInput)
        connectButton = findViewById(R.id.connectButton)
        statusText = findViewById(R.id.statusText)
        myKeyText = findViewById(R.id.myKeyText)

        ensureClientKey()
        loadSavedConfig()
        maybePromptBatteryExemption()

        connectButton.setOnClickListener {
            if (BondifyVpnService.status is BondifyVpnService.TunnelStatus.Disconnected ||
                BondifyVpnService.status is BondifyVpnService.TunnelStatus.Failed
            ) {
                requestConnect()
            } else {
                stopTunnel()
            }
        }
    }

    override fun onResume() {
        super.onResume()
        refreshStatus()
    }

    private fun ensureClientKey() {
        val prefs = getSharedPreferences(Prefs.NAME, Context.MODE_PRIVATE)
        if (Prefs.clientKey(prefs) == null) {
            val key = Mobile.generateKey()
            Prefs.setClientKey(prefs, key)
        }
        val pub = Mobile.publicKeyFor(Prefs.clientKey(prefs)!!)
        myKeyText?.text = getString(R.string.label_my_pubkey, pub)
    }

    private fun loadSavedConfig() {
        val prefs = getSharedPreferences(Prefs.NAME, Context.MODE_PRIVATE)
        relayAddrInput?.setText(prefs.getString(Prefs.KEY_RELAY_ADDR, ""))
        relayPubKeyInput?.setText(prefs.getString(Prefs.KEY_RELAY_PUBKEY, ""))
    }

    /**
     * The compact cover-screen layout has no address/pubkey fields (typing on a 3.4" screen
     * isn't practical), so it can only ever connect with whatever was last saved from the
     * full-size layout -- never write empty values back to prefs in that case.
     */
    private fun requestConnect() {
        val prefs = getSharedPreferences(Prefs.NAME, Context.MODE_PRIVATE)
        val addrInput = relayAddrInput
        val pubKeyInput = relayPubKeyInput
        val relayAddr: String
        val relayPubKey: String
        if (addrInput != null && pubKeyInput != null) {
            relayAddr = addrInput.text.toString().trim()
            relayPubKey = pubKeyInput.text.toString().trim()
            prefs.edit()
                .putString(Prefs.KEY_RELAY_ADDR, relayAddr)
                .putString(Prefs.KEY_RELAY_PUBKEY, relayPubKey)
                .apply()
        } else {
            relayAddr = prefs.getString(Prefs.KEY_RELAY_ADDR, "").orEmpty()
            relayPubKey = prefs.getString(Prefs.KEY_RELAY_PUBKEY, "").orEmpty()
        }
        if (relayAddr.isEmpty() || relayPubKey.isEmpty()) {
            val message = if (addrInput == null) {
                getString(R.string.hint_configure_on_main_screen)
            } else {
                "Enter a relay address and public key"
            }
            Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
            return
        }

        val consent = VpnService.prepare(this)
        if (consent != null) {
            vpnPermissionLauncher.launch(consent)
        } else {
            startTunnel()
        }
    }

    private fun startTunnel() {
        val prefs = getSharedPreferences(Prefs.NAME, Context.MODE_PRIVATE)
        val intent = Intent(this, BondifyVpnService::class.java).apply {
            action = BondifyVpnService.ACTION_CONNECT
            putExtra(BondifyVpnService.EXTRA_RELAY_ADDR, prefs.getString(Prefs.KEY_RELAY_ADDR, ""))
            putExtra(BondifyVpnService.EXTRA_RELAY_PUBKEY, prefs.getString(Prefs.KEY_RELAY_PUBKEY, ""))
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent)
        } else {
            startService(intent)
        }
        refreshStatus()
    }

    private fun stopTunnel() {
        val intent = Intent(this, BondifyVpnService::class.java).apply {
            action = BondifyVpnService.ACTION_DISCONNECT
        }
        startService(intent)
        refreshStatus()
    }

    private fun refreshStatus() {
        when (val s = BondifyVpnService.status) {
            is BondifyVpnService.TunnelStatus.Disconnected -> {
                statusText.text = getString(R.string.status_disconnected)
                connectButton.setText(R.string.btn_connect)
            }
            is BondifyVpnService.TunnelStatus.Connecting -> {
                statusText.text = getString(R.string.status_connecting)
                connectButton.setText(R.string.btn_disconnect)
            }
            is BondifyVpnService.TunnelStatus.Connected -> {
                statusText.text = getString(R.string.status_connected) + " (${s.tunnelIp}, ${s.pathCount} path${if (s.pathCount == 1) "" else "s"})"
                connectButton.setText(R.string.btn_disconnect)
            }
            is BondifyVpnService.TunnelStatus.Failed -> {
                statusText.text = getString(R.string.status_error, s.message)
                connectButton.setText(R.string.btn_connect)
            }
        }
    }

    /**
     * Doze/App Standby can suspend network access and kill background work outright once the
     * screen has been off long enough -- a foreground service raises priority but is not a
     * complete exemption on every OEM's power-management skin (this is exactly the risk
     * phase 5's 30-minute screen-off gate targets; see ARCHITECTURE.md §9 for what this repo
     * could and couldn't verify without a real device). Ask once; a user who declines can
     * still use the app, just with a higher chance of a screen-off disconnect on aggressive
     * OEM skins.
     */
    private fun maybePromptBatteryExemption() {
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M && !pm.isIgnoringBatteryOptimizations(packageName)) {
            val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS).apply {
                data = Uri.parse("package:$packageName")
            }
            runCatching { startActivity(intent) }
        }
    }
}
