package dev.bondify.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class RuntimeInterfaceSettingsTest {
    @Test
    fun defaultsCanSelectBothUplinksInSpeedMode() {
        val out = RuntimeInterfaceSettings.fromStoredValues(
            modeRaw = "speed",
            wifiEnabled = true,
            cellularEnabled = true,
        )

        assertEquals("speed", out.modeWireValue)
        assertEquals(setOf("cellular", "wifi"), out.enabledInterfaces)
        assertTrue(out.isEnabled("wifi"))
        assertTrue(out.isEnabled("cellular"))
    }

    @Test
    fun redundantModeAndSinglePathSelectionReachRuntime() {
        val out = RuntimeInterfaceSettings.fromStoredValues(
            modeRaw = "redundant",
            wifiEnabled = false,
            cellularEnabled = true,
        )

        assertEquals("redundant", out.modeWireValue)
        assertFalse(out.isEnabled("wifi"))
        assertTrue(out.isEnabled("cellular"))
    }

    @Test
    fun rejectsAllDisabledInsteadOfStartingAnEmptyTunnel() {
        assertThrows(IllegalArgumentException::class.java) {
            RuntimeInterfaceSettings.fromStoredValues(
                modeRaw = "speed",
                wifiEnabled = false,
                cellularEnabled = false,
            )
        }
    }

    @Test
    fun rejectsReservedAndUnknownModesBeforeTunnelConstruction() {
        listOf("stream", "custom", "turbo", "").forEach { mode ->
            assertThrows(IllegalArgumentException::class.java) {
                RuntimeInterfaceSettings.fromStoredValues(
                    modeRaw = mode,
                    wifiEnabled = true,
                    cellularEnabled = true,
                )
            }
        }
    }
}
