package com.smarterp.app

import android.util.Base64
import org.json.JSONObject
import java.math.BigInteger
import java.net.HttpURLConnection
import java.net.URL
import java.security.KeyPairGenerator
import java.security.Signature
import java.security.interfaces.ECPrivateKey
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.time.Instant
import java.util.UUID

object DeviceSession {
    fun signIn(baseUrl: String, loginName: String, password: String): String {
        val base = baseUrl.trimEnd('/')
        val pair = KeyPairGenerator.getInstance("EC").apply {
            initialize(ECGenParameterSpec("secp256r1"))
        }.generateKeyPair()
        val publicKey = pair.public as ECPublicKey
        val privateKey = pair.private as ECPrivateKey
        val jwk = publicJwk(publicKey)
        val enrolled = post("$base/api/v1/auth/device/enroll", JSONObject()
            .put("public_key", jwk)
            .put("platform", "android")
            .put("app_version", "0.1.0")
            .put("device_name", "emulator"))
        val deviceId = enrolled.getJSONObject("data").getString("device_id")
        val session = post("$base/api/v1/auth/session", JSONObject().put("login_name", loginName))
        val sessionId = session.getJSONObject("data").getString("session_id")
        val checked = post("$base/api/v1/auth/session/$sessionId/check", JSONObject().put("password", password))
        if (!checked.getJSONObject("data").optBoolean("verified")) {
            throw IllegalStateException("Sign-in was not verified")
        }
        val tokenUrl = "$base/api/v1/auth/token"
        val tokens = post(
            tokenUrl,
            JSONObject().put("session_id", sessionId).put("device_id", deviceId),
            dpop(privateKey, jwk, "POST", tokenUrl, null),
        )
        val access = tokens.getJSONObject("data").getString("access_token")
        val meUrl = "$base/api/v1/me"
        val me = get(meUrl, access, dpop(privateKey, jwk, "GET", meUrl, access))
        val name = me.getJSONObject("data").getString("name")
        if (name.isBlank()) {
            throw IllegalStateException("Current user name was empty")
        }
        return name
    }

    private fun publicJwk(key: ECPublicKey): JSONObject {
        val point = key.w
        return JSONObject()
            .put("kty", "EC")
            .put("crv", "P-256")
            .put("x", b64(fixed32(point.affineX)))
            .put("y", b64(fixed32(point.affineY)))
    }

    private fun dpop(key: ECPrivateKey, jwk: JSONObject, method: String, url: String, access: String?): String {
        val header = JSONObject().put("typ", "dpop+jwt").put("alg", "ES256").put("jwk", JSONObject(jwk.toString()))
        val payload = JSONObject()
            .put("htm", method)
            .put("htu", url)
            .put("iat", Instant.now().epochSecond)
            .put("jti", UUID.randomUUID().toString())
        if (access != null) {
            val digest = java.security.MessageDigest.getInstance("SHA-256").digest(access.toByteArray(Charsets.US_ASCII))
            payload.put("ath", b64(digest))
        }
        val signingInput = b64(header.toString().toByteArray(Charsets.UTF_8)) + "." + b64(payload.toString().toByteArray(Charsets.UTF_8))
        val signer = Signature.getInstance("SHA256withECDSA")
        signer.initSign(key)
        signer.update(signingInput.toByteArray(Charsets.US_ASCII))
        return signingInput + "." + b64(derToRaw(signer.sign()))
    }

    private fun post(url: String, body: JSONObject, dpop: String? = null): JSONObject {
        return call(url, "POST", body.toString(), null, dpop)
    }

    private fun get(url: String, access: String, dpop: String): JSONObject {
        return call(url, "GET", null, access, dpop)
    }

    private fun call(url: String, method: String, body: String?, access: String?, dpop: String?): JSONObject {
        val conn = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = method
            connectTimeout = 8_000
            readTimeout = 8_000
            setRequestProperty("Accept", "application/json")
            if (body != null) {
                doOutput = true
                setRequestProperty("Content-Type", "application/json")
                setRequestProperty("Idempotency-Key", UUID.randomUUID().toString())
            }
            if (access != null) {
                setRequestProperty("Authorization", "Bearer $access")
            }
            if (dpop != null) {
                setRequestProperty("DPoP", dpop)
            }
        }
        return try {
            if (body != null) {
                conn.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
            }
            val code = conn.responseCode
            val stream = if (code in 200..299) conn.inputStream else conn.errorStream
            val text = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
            if (code !in 200..299) {
                throw IllegalStateException("HTTP $code $text")
            }
            JSONObject(text)
        } finally {
            conn.disconnect()
        }
    }

    private fun b64(data: ByteArray): String =
        Base64.encodeToString(data, Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP)

    private fun fixed32(value: BigInteger): ByteArray {
        var raw = value.toByteArray()
        if (raw.size > 32 && raw[0] == 0.toByte()) {
            raw = raw.copyOfRange(1, raw.size)
        }
        if (raw.size > 32) {
            throw IllegalStateException("coordinate is ${raw.size} bytes")
        }
        val out = ByteArray(32)
        System.arraycopy(raw, 0, out, 32 - raw.size, raw.size)
        return out
    }

    private fun derToRaw(der: ByteArray): ByteArray {
        var i = 0
        if (der[i++] != 0x30.toByte()) {
            throw IllegalStateException("DPoP signature is not DER")
        }
        i = skipLength(der, i)
        val r = readInteger(der, i)
        val s = readInteger(der, r.next)
        return r.value + s.value
    }

    private data class IntegerPart(val value: ByteArray, val next: Int)

    private fun skipLength(der: ByteArray, start: Int): Int {
        val first = der[start].toInt() and 0xff
        return if (first and 0x80 == 0) start + 1 else start + 1 + (first and 0x7f)
    }

    private fun readInteger(der: ByteArray, start: Int): IntegerPart {
        var i = start
        if (der[i++] != 0x02.toByte()) {
            throw IllegalStateException("DPoP signature integer missing")
        }
        val first = der[i].toInt() and 0xff
        val length: Int
        if (first and 0x80 == 0) {
            length = first
            i += 1
        } else {
            val count = first and 0x7f
            length = 0
            i += 1
            // Short signatures from P-256 never need a long length form.
            if (count != 0) {
                throw IllegalStateException("unexpected integer length")
            }
        }
        var raw = der.copyOfRange(i, i + length)
        if (raw.size > 32 && raw[0] == 0.toByte()) {
            raw = raw.copyOfRange(1, raw.size)
        }
        if (raw.size > 32) {
            throw IllegalStateException("signature integer is ${raw.size} bytes")
        }
        val out = ByteArray(32)
        System.arraycopy(raw, 0, out, 32 - raw.size, raw.size)
        return IntegerPart(out, i + length)
    }
}
