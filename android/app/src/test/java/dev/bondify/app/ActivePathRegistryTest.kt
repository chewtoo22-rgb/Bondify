package dev.bondify.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ActivePathRegistryTest {
    @Test
    fun staleLossCannotRemoveReplacementNetwork() {
        val registry = ActivePathRegistry<String>()
        registry.replace("wifi", "wifi-old")
        registry.replace("wifi", "wifi-new")

        assertFalse(registry.removeIfCurrent("wifi", "wifi-old"))
        assertEquals("wifi-new", registry.current("wifi"))
        assertTrue(registry.removeIfCurrent("wifi", "wifi-new"))
        assertNull(registry.current("wifi"))
    }

    @Test
    fun transportsRemainIndependent() {
        val registry = ActivePathRegistry<String>()
        registry.replace("wifi", "wifi-1")
        registry.replace("cellular", "cell-1")

        assertTrue(registry.removeIfCurrent("wifi", "wifi-1"))
        assertEquals("cell-1", registry.current("cellular"))
    }

    @Test
    fun snapshotIsStableCopy() {
        val registry = ActivePathRegistry<String>()
        registry.replace("wifi", "wifi-1")
        val snapshot = registry.snapshot()
        registry.replace("wifi", "wifi-2")

        assertEquals("wifi-1", snapshot["wifi"])
        assertEquals("wifi-2", registry.current("wifi"))
    }

    @Test(expected = IllegalArgumentException::class)
    fun blankLabelsFailClosed() {
        ActivePathRegistry<String>().replace("   ", "network")
    }
}
