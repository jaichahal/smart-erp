package com.smarterp.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import androidx.test.ext.junit.runners.AndroidJUnit4

/**
 * A physical phone cannot use the emulator host 10.0.2.2.
 * adb reverse tcp:8080 tcp:8080 lets the phone reach the Mac at 127.0.0.1:8080.
 * Builds omit apiBaseUrl and keep the emulator URL.
 */
@RunWith(AndroidJUnit4::class)
class ApiBaseOverrideTest {
    @Test
    fun blankOverrideKeepsEmulatorHost() {
        assertEquals("http://10.0.2.2:8080", resolveApiBaseUrl(null, "http://10.0.2.2:8080"))
        assertEquals("http://10.0.2.2:8080", resolveApiBaseUrl("  ", "http://10.0.2.2:8080/"))
    }

    @Test
    fun overrideReplacesEmulatorHost() {
        assertEquals(
            "http://127.0.0.1:8080",
            resolveApiBaseUrl("http://127.0.0.1:8080/", "http://10.0.2.2:8080"),
        )
    }

    @Test
    fun installedBuildUsesTheSameRule() {
        assertEquals(
            resolveApiBaseUrl(BuildConfig.API_BASE_OVERRIDE, BuildConfig.API_BASE_URL),
            currentApiBaseUrl(),
        )
        assertTrue(BuildConfig.API_BASE_URL.contains("10.0.2.2"))
        if (BuildConfig.API_BASE_OVERRIDE.isNotBlank()) {
            assertEquals("http://127.0.0.1:8080", currentApiBaseUrl())
        }
    }
}
