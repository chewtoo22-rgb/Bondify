package dev.bondify.app

/**
 * Converts persisted Android preferences into the portable interface-settings contract used by
 * the VPN runtime. Android currently knows how to acquire only Wi-Fi and cellular uplinks, so
 * unknown interface selectors fail closed instead of being silently ignored.
 *
 * [Selection] is intentionally a plain public value type because [Prefs] exposes it to callers;
 * the internal contract implementation remains hidden behind [fromStoredValues].
 */
object RuntimeInterfaceSettings {
    const val WIFI = "wifi"
    const val CELLULAR = "cellular"

    data class Selection(
        val modeWireValue: String,
        val enabledInterfaces: Set<String>,
    ) {
        fun isEnabled(interfaceId: String): Boolean = interfaceId in enabledInterfaces
    }

    fun fromStoredValues(
        modeRaw: String,
        wifiEnabled: Boolean,
        cellularEnabled: Boolean,
    ): Selection {
        val config = InterfaceSettingsContract.normalize(
            InterfaceSettingsContract.Config(
                schemaVersion = InterfaceSettingsContract.SCHEMA_VERSION,
                mode = InterfaceSettingsContract.Mode.fromWireValue(modeRaw),
                interfaces = listOf(
                    InterfaceSettingsContract.InterfacePreference(WIFI, wifiEnabled),
                    InterfaceSettingsContract.InterfacePreference(CELLULAR, cellularEnabled),
                ),
            )
        )

        val known = setOf(WIFI, CELLULAR)
        require(config.interfaces.all { it.id in known }) {
            "Android runtime contains an unsupported interface selector"
        }

        return Selection(
            modeWireValue = config.mode.wireValue,
            enabledInterfaces = config.interfaces.filter { it.enabled }.mapTo(linkedSetOf()) { it.id },
        )
    }
}
