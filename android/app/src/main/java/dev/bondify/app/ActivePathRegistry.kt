package dev.bondify.app

/**
 * Thread-safe identity registry for Android physical uplinks.
 *
 * ConnectivityManager can deliver an onAvailable(new) callback before a delayed onLost(old)
 * callback for the same transport. Path labels are intentionally stable ("wifi", "cellular"),
 * so callers must compare network identity before acting on loss or they can tear down the
 * replacement path. This tiny registry keeps that ordering rule testable without Android APIs.
 */
internal class ActivePathRegistry<T : Any> {
    private val active = mutableMapOf<String, T>()

    @Synchronized
    fun replace(label: String, value: T): T? {
        require(label.isNotBlank()) { "path label must not be blank" }
        return active.put(label, value)
    }

    @Synchronized
    fun removeIfCurrent(label: String, value: T): Boolean {
        if (active[label] != value) {
            return false
        }
        active.remove(label)
        return true
    }

    @Synchronized
    fun current(label: String): T? = active[label]

    @Synchronized
    fun snapshot(): Map<String, T> = active.toMap()

    @Synchronized
    fun clear() {
        active.clear()
    }
}
