package com.smarterp.app

/**
 * Emulator builds keep http://10.0.2.2. A physical phone passes apiBaseUrl
 * (http://127.0.0.1:8080) after `adb reverse tcp:8080 tcp:8080`.
 */
fun resolveApiBaseUrl(override: String?, emulatorDefault: String): String {
    val chosen = override?.trim().orEmpty()
    return if (chosen.isEmpty()) emulatorDefault.trimEnd('/') else chosen.trimEnd('/')
}

fun currentApiBaseUrl(): String = resolveApiBaseUrl(BuildConfig.API_BASE_OVERRIDE, BuildConfig.API_BASE_URL)
