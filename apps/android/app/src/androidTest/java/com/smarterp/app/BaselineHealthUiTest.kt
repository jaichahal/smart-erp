package com.smarterp.app

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.json.JSONObject
import org.junit.Assert.fail
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.net.HttpURLConnection
import java.net.URL

/**
 * Connected UI test against the running baseline API.
 * Branched from integration/e2e @ 8d797bdc930eb4b28e498f4645f87684e4d2e92f.
 *
 * GET /health is the public readiness route mounted in apps/api/internal/app/router.go.
 * The emulator reaches the Mac at 10.0.2.2 on the host port published by the api
 * service in deploy/compose/docker-compose.yml.
 */
@RunWith(AndroidJUnit4::class)
class BaselineHealthUiTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    @Test
    fun showsLiveBaselineHealthStatus() {
        val base = currentApiBaseUrl()
        val status = baselineHealthStatus(base)
        try {
            compose.waitUntil(timeoutMillis = 20_000) {
                compose.onAllNodesWithTag("health-status").fetchSemanticsNodes().isNotEmpty()
            }
        } catch (error: Throwable) {
            fail(
                "expected the screen to show baseline health status '$status' from $base/health; " +
                    "no node with testTag health-status. ${error.message}",
            )
        }
        compose.onNodeWithTag("health-status").assertIsDisplayed().assertTextEquals(status)
    }

    private fun baselineHealthStatus(base: String): String {
        val url = "$base/health"
        val conn = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = 5_000
            readTimeout = 5_000
        }
        try {
            val code = conn.responseCode
            val stream = if (code in 200..299) conn.inputStream else conn.errorStream
            val body = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
            if (code != 200) {
                fail("baseline API is not reachable at $url: HTTP $code $body")
            }
            val status = JSONObject(body).getJSONObject("data").getString("status")
            if (status.isBlank()) {
                fail("baseline API at $url returned an empty health status")
            }
            return status
        } catch (error: AssertionError) {
            throw error
        } catch (error: Exception) {
            fail("baseline API is not reachable at $url: ${error.javaClass.simpleName}: ${error.message}")
        }
        error("unreachable")
    }
}
