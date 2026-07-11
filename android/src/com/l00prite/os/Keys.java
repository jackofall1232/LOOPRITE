package com.l00prite.os;

import android.content.Context;
import android.content.SharedPreferences;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;

import java.security.KeyStore;
import java.security.SecureRandom;

import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/**
 * Master-key and setup-secret management for the on-device gateway (see
 * cli-os/docs/android-architecture.md, gap G6/G7).
 *
 * The 32-byte LOOPRITE_MASTER_KEY is generated once, wrapped with a non-exportable
 * AES-256-GCM key that lives only inside the Android Keystore, and only the wrapped
 * ciphertext is ever written to disk (app-private SharedPreferences). The raw key never
 * touches storage: every boot, GatewayService calls getOrCreateMasterKeyBase64() to
 * unwrap it back into memory and hands it to the gateway process as an environment
 * variable only -- master.key never exists as a file on this device.
 */
final class Keys {

    private static final String PREFS = "l00prite";
    private static final String KEYSTORE_ALIAS = "l00prite-master-key-wrap";
    private static final String PREF_WRAPPED_KEY = "master_key_wrapped";
    private static final String PREF_SETUP_SECRET = "setup_secret";
    private static final int RAW_KEY_BYTES = 32;
    private static final int GCM_IV_BYTES = 12;
    private static final int GCM_TAG_BITS = 128;

    private Keys() {
    }

    /**
     * Returns the standard-base64-encoded 32-byte master key for LOOPRITE_MASTER_KEY,
     * generating and wrapping it on the very first call. Safe to call on every boot.
     */
    static String getOrCreateMasterKeyBase64(Context context) {
        SharedPreferences prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
        String wrapped = prefs.getString(PREF_WRAPPED_KEY, null);
        try {
            SecretKey wrapKey = getOrCreateWrapKey();
            byte[] rawKey;
            if (wrapped == null) {
                rawKey = new byte[RAW_KEY_BYTES];
                new SecureRandom().nextBytes(rawKey);
                byte[] combined = encrypt(wrapKey, rawKey);
                // commit(), not apply(): the raw key below is about to be handed to the gateway
                // process and used to encrypt provider secrets immediately. apply() only queues the
                // disk write asynchronously and gives no failure signal -- if the app/process is
                // killed, or the write fails, before it lands, the next boot generates a NEW master
                // key and every secret encrypted this session becomes permanently undecryptable.
                // commit() blocks until the write actually lands (or fails), so a failure can be
                // caught here instead of silently corrupting state (Codex review, PR #7).
                boolean persisted = prefs.edit()
                        .putString(PREF_WRAPPED_KEY, Base64.encodeToString(combined, Base64.NO_WRAP))
                        .commit();
                if (!persisted) {
                    throw new RuntimeException("l00prite: failed to persist the wrapped master key; refusing to hand out a key that may not survive a restart");
                }
            } else {
                byte[] combined = Base64.decode(wrapped, Base64.NO_WRAP);
                rawKey = decrypt(wrapKey, combined);
            }
            return Base64.encodeToString(rawKey, Base64.NO_WRAP);
        } catch (Exception e) {
            throw new RuntimeException("l00prite: master key unwrap failed", e);
        }
    }

    /**
     * Returns the per-install setup secret, generating it on the first call: 32 random
     * hex characters persisted in the same app-private SharedPreferences.
     *
     * This gates only the pre-authentication first-run setup endpoints (gap G7): the
     * wizard page reads it once from its own launch URL query string and never persists
     * it client-side, and once setup latches, the admin Bearer token issued by the
     * wizard is the real long-term credential for everything else. Storing this value
     * in *plain* (unencrypted) SharedPreferences -- unlike the master key above -- is
     * an intentional, acceptable trade-off: it is not a long-term credential, its entire
     * useful lifetime ends the moment first-run setup completes, and the preferences
     * file is already private to this app's UID under standard Android sandboxing.
     */
    static String getOrCreateSetupSecret(Context context) {
        SharedPreferences prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
        String secret = prefs.getString(PREF_SETUP_SECRET, null);
        if (secret == null) {
            byte[] raw = new byte[16];
            new SecureRandom().nextBytes(raw);
            StringBuilder hex = new StringBuilder(raw.length * 2);
            for (int i = 0; i < raw.length; i++) {
                int v = raw[i] & 0xff;
                if (v < 0x10) {
                    hex.append('0');
                }
                hex.append(Integer.toHexString(v));
            }
            secret = hex.toString();
            prefs.edit().putString(PREF_SETUP_SECRET, secret).apply();
        }
        return secret;
    }

    private static SecretKey getOrCreateWrapKey() throws Exception {
        KeyStore keyStore = KeyStore.getInstance("AndroidKeyStore");
        keyStore.load(null);
        if (!keyStore.containsAlias(KEYSTORE_ALIAS)) {
            KeyGenParameterSpec spec = new KeyGenParameterSpec.Builder(
                    KEYSTORE_ALIAS,
                    KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                    .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                    .setKeySize(256)
                    .build();
            KeyGenerator generator = KeyGenerator.getInstance(
                    KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
            generator.init(spec);
            return generator.generateKey();
        }
        return (SecretKey) keyStore.getKey(KEYSTORE_ALIAS, null);
    }

    private static byte[] encrypt(SecretKey key, byte[] plain) throws Exception {
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        cipher.init(Cipher.ENCRYPT_MODE, key);
        byte[] iv = cipher.getIV();
        byte[] ciphertext = cipher.doFinal(plain);
        byte[] combined = new byte[iv.length + ciphertext.length];
        System.arraycopy(iv, 0, combined, 0, iv.length);
        System.arraycopy(ciphertext, 0, combined, iv.length, ciphertext.length);
        return combined;
    }

    private static byte[] decrypt(SecretKey key, byte[] combined) throws Exception {
        byte[] iv = new byte[GCM_IV_BYTES];
        byte[] ciphertext = new byte[combined.length - GCM_IV_BYTES];
        System.arraycopy(combined, 0, iv, 0, GCM_IV_BYTES);
        System.arraycopy(combined, GCM_IV_BYTES, ciphertext, 0, ciphertext.length);
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        cipher.init(Cipher.DECRYPT_MODE, key, new GCMParameterSpec(GCM_TAG_BITS, iv));
        return cipher.doFinal(ciphertext);
    }
}
