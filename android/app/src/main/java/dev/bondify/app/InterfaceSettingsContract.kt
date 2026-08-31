package dev.bondify.app

/**
 * Android-side mirror of core/settings' portable interface-selection contract.
 *
 * This layer deliberately contains no addresses, SSIDs, endpoints, keys, or tokens. It exists
 * so Android UI/runtime code can validate and canonicalize user selections before they cross the
 * gomobile boundary into the Go core.
 */
internal object InterfaceSettingsContract {
    const val SCHEMA_VERSION = 1
    const val MAX_INTERFACES = 8
    const val MAX_INTERFACE_ID_CODEPOINTS = 64

    enum class Mode(val wireValue: String) {
        SPEED("speed"),
        REDUNDANT("redundant");

        companion object {
            fun fromWireValue(raw: String): Mode = when (raw.trim()) {
                SPEED.wireValue -> SPEED
                REDUNDANT.wireValue -> REDUNDANT
                "stream", "custom" -> throw IllegalArgumentException(
                    "mode ${raw.trim()} is reserved but not implemented"
                )
                else -> throw IllegalArgumentException("unknown mode ${raw.trim()}")
            }
        }
    }

    data class InterfacePreference(
        val id: String,
        val enabled: Boolean,
    )

    data class Config(
        val schemaVersion: Int,
        val mode: Mode,
        val interfaces: List<InterfacePreference>,
    )

    fun normalize(config: Config): Config {
        require(config.schemaVersion == SCHEMA_VERSION) {
            "unsupported schema version ${config.schemaVersion}"
        }
        require(config.interfaces.isNotEmpty()) { "at least one interface is required" }
        require(config.interfaces.size <= MAX_INTERFACES) {
            "interface count ${config.interfaces.size} exceeds $MAX_INTERFACES"
        }

        val seen = HashSet<String>(config.interfaces.size)
        var enabledCount = 0
        val normalized = config.interfaces.map { pref ->
            val id = normalizeInterfaceId(pref.id)
            require(seen.add(id)) { "duplicate interface $id" }
            if (pref.enabled) enabledCount++
            InterfacePreference(id = id, enabled = pref.enabled)
        }

        require(enabledCount > 0) { "at least one interface must be enabled" }

        return Config(
            schemaVersion = SCHEMA_VERSION,
            mode = config.mode,
            interfaces = normalized.sortedBy { it.id },
        )
    }

    private fun normalizeInterfaceId(raw: String): String {
        val id = raw.trim()
        require(id.isNotEmpty()) { "interface ID is empty" }
        require(id.codePointCount(0, id.length) <= MAX_INTERFACE_ID_CODEPOINTS) {
            "interface ID exceeds $MAX_INTERFACE_ID_CODEPOINTS code points"
        }
        require(id.none { it.isISOControl() }) { "interface ID contains control characters" }
        return id
    }
}
