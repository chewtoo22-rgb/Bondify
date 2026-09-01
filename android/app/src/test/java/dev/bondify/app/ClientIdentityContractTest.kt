package dev.bondify.app

import java.util.Base64
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class ClientIdentityContractTest {
    private fun decode(value: String): ByteArray = Base64.getDecoder().decode(value)

    @Test
    fun acceptsCanonical32ByteKey() {
        val key = Base64.getEncoder().encodeToString(ByteArray(32) { it.toByte() })
        assertEquals(key, ClientIdentityContract.validateBase64(key, ::decode))
    }

    @Test
    fun rejectsWrongDecodedLength() {
        val key = Base64.getEncoder().encodeToString(ByteArray(31))
        assertThrows(IllegalArgumentException::class.java) {
            ClientIdentityContract.validateBase64(key, ::decode)
        }
    }

    @Test
    fun rejectsMalformedBase64AtCanonicalLength() {
        val malformed = "!".repeat(43) + "="
        assertThrows(IllegalArgumentException::class.java) {
            ClientIdentityContract.validateBase64(malformed, ::decode)
        }
    }

    @Test
    fun rejectsWhitespaceAndControlCharacters() {
        val valid = Base64.getEncoder().encodeToString(ByteArray(32))
        val whitespace = valid.substring(0, 10) + "\n" + valid.substring(11)
        assertThrows(IllegalArgumentException::class.java) {
            ClientIdentityContract.validateBase64(whitespace, ::decode)
        }

        val control = valid.substring(0, 10) + "\u0000" + valid.substring(11)
        assertThrows(IllegalArgumentException::class.java) {
            ClientIdentityContract.validateBase64(control, ::decode)
        }
    }

    @Test
    fun rejectsEmptyAndOversizedValuesBeforeDecode() {
        var decodeCalled = false
        val decoder: (String) -> ByteArray = {
            decodeCalled = true
            ByteArray(32)
        }

        assertThrows(IllegalArgumentException::class.java) {
            ClientIdentityContract.validateBase64("", decoder)
        }
        assertEquals(false, decodeCalled)

        assertThrows(IllegalArgumentException::class.java) {
            ClientIdentityContract.validateBase64("A".repeat(4096), decoder)
        }
        assertEquals(false, decodeCalled)
    }
}
