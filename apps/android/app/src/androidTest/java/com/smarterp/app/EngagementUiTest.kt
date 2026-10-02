package com.smarterp.app

import android.content.Context
import androidx.compose.ui.test.assertTextContains
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class EngagementUiTest {
    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    @Test
    fun phoneNumberIsTheFirstQuestion() {
        compose.onNodeWithTag("onboarding-progress").assertTextEquals("1 of 3")
        compose.onNodeWithTag("country-code").assertExists()
        compose.onNodeWithTag("phone-number").assertExists()
        assertTrue(compose.onAllNodesWithTag("login-name").fetchSemanticsNodes().isEmpty())
        assertTrue(compose.onAllNodesWithTag("camera-permission").fetchSemanticsNodes().isEmpty())
        assertTrue(compose.onAllNodesWithTag("notification-permission").fetchSemanticsNodes().isEmpty())
    }

    @Test
    fun codeStepUsesTheSessionApi() {
        compose.onNodeWithTag("phone-number").performTextInput("501234567")
        compose.onNodeWithTag("phone-continue").performClick()
        compose.onNodeWithTag("onboarding-progress").assertTextEquals("2 of 3")
        compose.onNodeWithTag("otp-code").performTextInput("000000")
        compose.onNodeWithTag("otp-verify").performClick()
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodesWithTag("otp-error").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("otp-error").assertTextContains("HTTP", substring = true)
        assertTrue(compose.onAllNodesWithTag("signed-in-name").fetchSemanticsNodes().isEmpty())
        assertTrue(compose.onAllNodesWithTag("profile-name").fetchSemanticsNodes().isEmpty())
    }

    @Test
    fun themeFollowsAnInAppOverride() {
        assertFalse(prefersDark("light", systemDark = true))
        assertTrue(prefersDark("dark", systemDark = false))
        assertTrue(prefersDark("system", systemDark = true))
        assertEquals("http://127.0.0.1:8080", activeBaseUrl("http://127.0.0.1:8080/", "http://10.0.2.2:8080"))
        assertEquals("http://10.0.2.2:8080", activeBaseUrl("  ", "http://10.0.2.2:8080/"))
    }

    @Test
    fun settingsTogglesPersist() {
        signIn()
        compose.onNodeWithTag("Settings").performScrollTo().performClick()
        compose.onNodeWithTag("settings-screen").assertExists()
        compose.onNodeWithTag("sync-status").assertExists()
        compose.onNodeWithTag("app-version").assertTextEquals("0.1.0")
        compose.onNodeWithTag("theme-dark").performScrollTo().performClick()
        compose.onNodeWithTag("theme-value").assertTextEquals("dark")
        compose.onNodeWithTag("theme-applied").assertTextEquals("dark")
        compose.onNodeWithTag("biometric-lock").performScrollTo().performClick()
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodesWithTag("biometric-error").fetchSemanticsNodes().isNotEmpty() ||
                compose.onAllNodesWithTag("biometric-prompt").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("biometric-prompt").assertExists()
        compose.onNodeWithTag("notify-approvals").performScrollTo().performClick()
        compose.onNodeWithTag("direction-rtl").performScrollTo().performClick()
        compose.onNodeWithTag("direction-value").assertTextEquals("rtl")
        compose.onNodeWithTag("server-url").performScrollTo().performTextInput(currentApiBaseUrl())
        compose.onNodeWithTag("save-server").performScrollTo().performClick()
        val prefs = InstrumentationRegistry.getInstrumentation().targetContext
            .getSharedPreferences("smarterp-settings", Context.MODE_PRIVATE)
        assertEquals("dark", prefs.getString("theme", null))
        assertEquals(true, prefs.getBoolean("biometric_lock", false))
        assertEquals(false, prefs.getBoolean("notify_approvals", true))
        assertEquals("rtl", prefs.getString("direction", null))
        assertTrue(prefs.getString("server_url", "").orEmpty().isNotBlank())
        compose.activityRule.scenario.recreate()
        compose.onNodeWithTag("theme-applied").assertTextEquals("dark")
    }

    private fun signIn() {
        compose.onNodeWithTag("use-work-email").performClick()
        compose.onNodeWithTag("login-name").performTextInput("admin@dev.localhost")
        compose.onNodeWithTag("password").performTextInput("Admin1234!")
        compose.onNodeWithTag("sign-in").performClick()
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodesWithTag("signed-in-name").fetchSemanticsNodes().isNotEmpty()
        }
    }
}
