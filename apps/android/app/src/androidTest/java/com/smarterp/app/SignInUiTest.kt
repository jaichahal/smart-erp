package com.smarterp.app

import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.fail
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.net.HttpURLConnection
import java.net.URL

/**
 * Signs in on the emulator against the Docker API on the Mac (10.0.2.2).
 * The directory user is the Zitadel dev human seeded into erp.identity_users.
 * Password matches the documented dev value in deploy/compose/.env.dev.example.
 */
@RunWith(AndroidJUnit4::class)
class SignInUiTest {
    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    @Test
    fun signsInAndShowsDirectoryUserFromDockerApi() {
        val base = BuildConfig.API_BASE_URL.trimEnd('/')
        val health = get("$base/health")
        if (health.code != 200 || !health.body.contains("\"ready\"")) {
            fail("baseline API is not reachable at $base/health: HTTP ${health.code} ${health.body}")
        }
        compose.onNodeWithTag("login-name").performTextInput("admin@dev.localhost")
        compose.onNodeWithTag("password").performTextInput("Admin1234!")
        compose.onNodeWithTag("sign-in").performClick()
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodesWithTag("signed-in-name").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("signed-in-name").assertTextEquals("Dev Admin")
    }
}

private data class HttpResult(val code: Int, val body: String)

private fun get(url: String): HttpResult {
    val conn = (URL(url).openConnection() as HttpURLConnection).apply {
        requestMethod = "GET"
        connectTimeout = 8_000
        readTimeout = 8_000
    }
    return try {
        val code = conn.responseCode
        val stream = if (code in 200..299) conn.inputStream else conn.errorStream
        HttpResult(code, stream?.bufferedReader()?.use { it.readText() }.orEmpty())
    } finally {
        conn.disconnect()
    }
}
