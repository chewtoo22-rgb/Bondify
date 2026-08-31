package dev.bondify.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class InterfaceSettingsContractTest {
    @Test
    fun normalizesAndSortsInterfacesDeterministically() {
        val out = InterfaceSettingsContract.normalize(
            InterfaceSettingsContract.Config(
                schemaVersion = 1,
                mode = InterfaceSettingsContract.Mode.SPEED,
                interfaces = listOf(
                    InterfaceSettingsContract.InterfacePreference(" cellular ", true),
                    InterfaceSettingsContract.InterfacePreference("wifi", true),
                ),
            )
        )

        assertEquals(listOf("cellular", "wifi"), out.interfaces.map { it.id })
    }

    @Test
    fun acceptsOnlyImplementedModes() {
        assertEquals(
            InterfaceSettingsContract.Mode.REDUNDANT,
            InterfaceSettingsContract.Mode.fromWireValue("redundant")
        )
        assertThrows(IllegalArgumentException::class.java) {
            InterfaceSettingsContract.Mode.fromWireValue("stream")
        }
        assertThrows(IllegalArgumentException::class.java) {
            InterfaceSettingsContract.Mode.fromWireValue("custom")
        }
        assertThrows(IllegalArgumentException::class.java) {
            InterfaceSettingsContract.Mode.fromWireValue("turbo")
        }
    }

    @Test
    fun rejectsDuplicateSelectorsAfterWhitespaceNormalization() {
        assertThrows(IllegalArgumentException::class.java) {
            InterfaceSettingsContract.normalize(
                configOf(
                    InterfaceSettingsContract.InterfacePreference("wifi", true),
                    InterfaceSettingsContract.InterfacePreference(" wifi ", false),
                )
            )
        }
    }

    @Test
    fun rejectsAllDisabledConfiguration() {
        assertThrows(IllegalArgumentException::class.java) {
            InterfaceSettingsContract.normalize(
                configOf(InterfaceSettingsContract.InterfacePreference("wifi", false))
            )
        }
    }

    @Test
    fun rejectsSchemaDriftAndInterfaceCountOverflow() {
        assertThrows(IllegalArgumentException::class.java) {
            InterfaceSettingsContract.normalize(
                InterfaceSettingsContract.Config(
                    schemaVersion = 2,
                    mode = InterfaceSettingsContract.Mode.SPEED,
                    interfaces = listOf(
                        InterfaceSettingsContract.InterfacePreference("wifi", true)
                    ),
                )
            )
        }

        assertThrows(IllegalArgumentException::class.java) {
            InterfaceSettingsContract.normalize(
                configOf(*Array(9) { index ->
                    InterfaceSettingsContract.InterfacePreference("if$index", index == 0)
                })
            )
        }
    }

    @Test
    fun rejectsBlankControlCharacterAndOverlongIds() {
        listOf("   ", "wi\u0000fi", "x".repeat(65)).forEach { id ->
            assertThrows(IllegalArgumentException::class.java) {
                InterfaceSettingsContract.normalize(
                    configOf(InterfaceSettingsContract.InterfacePreference(id, true))
                )
            }
        }
    }

    @Test
    fun countsUnicodeByCodePointNotUtf16Units() {
        val id = "😀".repeat(64)
        val out = InterfaceSettingsContract.normalize(
            configOf(InterfaceSettingsContract.InterfacePreference(id, true))
        )
        assertEquals(id, out.interfaces.single().id)

        assertThrows(IllegalArgumentException::class.java) {
            InterfaceSettingsContract.normalize(
                configOf(InterfaceSettingsContract.InterfacePreference("😀".repeat(65), true))
            )
        }
    }

    private fun configOf(
        vararg interfaces: InterfaceSettingsContract.InterfacePreference,
    ) = InterfaceSettingsContract.Config(
        schemaVersion = InterfaceSettingsContract.SCHEMA_VERSION,
        mode = InterfaceSettingsContract.Mode.SPEED,
        interfaces = interfaces.toList(),
    )
}
