package dev.bondify.app

import android.content.SharedPreferences
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Stores Bondify's long-lived client private key encrypted with an AES key that never leaves
 * Android Keystore. SharedPreferences contains only AES-GCM ciphertext + IV. Existing installs
 * are migrated in place from the legacy plaintext preference on first successful read.
 *
 * Keystore/decryption failures are intentionally surfaced to the caller. Silently generating a
 * replacement identity would break relay authorization and make a security/storage failure look
 * like a normal first run.
 */
internal object ClientKeyStore {
    private const val ANDROID_KEY_STORE = "AndroidKeyStore"
    private const val KEY_ALIAS = "bondify_client_identity_wrap_v1"
    private const val LEGACY_KEY = "client_key_b64"
    private const val CIPHERTEXT_KEY = "client_key_ciphertext_v1"
    private const val IV_KEY = "client_key_iv_v1"
    private const val TRANSFORMATION = "AES/GCM/NoPadding"
    private const val TAG_BITS = 128

    @Synchronized
    fun getOrMigrate(prefs: SharedPreferences): String? {
        val ciphertext = prefs.getString(CIPHERTEXT_KEY, null)
        val iv = prefs.getString(IV_KEY, null)
        if (ciphertext != null || iv != null) {
            require(ciphertext != null && iv != null) {
                "Bondify encrypted client identity is incomplete"
            }
            return validateIdentity(decrypt(ciphertext, iv))
        }

        val legacy = prefs.getString(LEGACY_KEY, null) ?: return null
        val validated = validateIdentity(legacy)
        store(prefs, validated)
        return validated
    }

    @Synchronized
    fun store(prefs: SharedPreferences, value: String) {
        val validated = validateIdentity(value)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, getOrCreateWrappingKey())
        val encrypted = cipher.doFinal(validated.toByteArray(Charsets.UTF_8))

        val committed = prefs.edit()
            .putString(CIPHERTEXT_KEY, Base64.encodeToString(encrypted, Base64.NO_WRAP))
            .putString(IV_KEY, Base64.encodeToString(cipher.iv, Base64.NO_WRAP))
            .remove(LEGACY_KEY)
            .commit()
        check(committed) { "failed to persist encrypted Bondify client identity" }
    }

    private fun validateIdentity(value: String): String =
        ClientIdentityContract.validateBase64(value) { encoded ->
            Base64.decode(encoded, Base64.NO_WRAP)
        }

    private fun decrypt(ciphertextB64: String, ivB64: String): String {
        val encrypted = Base64.decode(ciphertextB64, Base64.NO_WRAP)
        val iv = Base64.decode(ivB64, Base64.NO_WRAP)
        require(iv.isNotEmpty()) { "Bondify client identity IV is empty" }

        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(
            Cipher.DECRYPT_MODE,
            getExistingWrappingKey(),
            GCMParameterSpec(TAG_BITS, iv),
        )
        return cipher.doFinal(encrypted).toString(Charsets.UTF_8)
    }

    private fun getExistingWrappingKey(): SecretKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEY_STORE).apply { load(null) }
        return (keyStore.getKey(KEY_ALIAS, null) as? SecretKey)
            ?: error("Bondify Keystore wrapping key is missing")
    }

    private fun getOrCreateWrappingKey(): SecretKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEY_STORE).apply { load(null) }
        (keyStore.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }

        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEY_STORE)
        generator.init(
            KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .build(),
        )
        return generator.generateKey()
    }
}
