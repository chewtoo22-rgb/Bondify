package dev.bondify.app

/**
 * Pure validation contract for the long-lived X25519 client identity passed to the Go core.
 * Keeping this Android-free makes the security boundary directly unit-testable on JVM CI.
 */
internal object ClientIdentityContract {
    private const val ENCODED_KEY_LENGTH = 44
    private const val DECODED_KEY_LENGTH = 32

    fun validateBase64(value: String, decode: (String) -> ByteArray): String {
        require(value.length == ENCODED_KEY_LENGTH) {
            "Bondify client identity must be a canonical 32-byte base64 key"
        }
        require(value.none { it.isWhitespace() || it.code < 0x20 || it.code == 0x7f }) {
            "Bondify client identity cannot contain whitespace or control characters"
        }

        val decoded = runCatching { decode(value) }
            .getOrElse { throw IllegalArgumentException("Bondify client identity is not valid base64", it) }
        require(decoded.size == DECODED_KEY_LENGTH) {
            "Bondify client identity must decode to exactly 32 bytes"
        }
        return value
    }
}
