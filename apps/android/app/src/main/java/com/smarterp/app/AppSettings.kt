package com.smarterp.app

import android.content.SharedPreferences

data class AppSettings(
    val theme: String = "system",
    val biometricLock: Boolean = false,
    val notifyApprovals: Boolean = true,
    val notifyPayments: Boolean = true,
    val direction: String = "ltr",
    val serverUrl: String = "",
)

fun activeBaseUrl(pref: String, builtIn: String): String {
    val chosen = pref.trim()
    return if (chosen.isEmpty()) builtIn.trimEnd('/') else chosen.trimEnd('/')
}

fun prefersDark(theme: String, systemDark: Boolean): Boolean = when (theme) {
    "dark" -> true
    "light" -> false
    else -> systemDark
}

fun loadSettings(prefs: SharedPreferences): AppSettings = AppSettings(
    theme = prefs.getString("theme", "system") ?: "system",
    biometricLock = prefs.getBoolean("biometric_lock", false),
    notifyApprovals = prefs.getBoolean("notify_approvals", true),
    notifyPayments = prefs.getBoolean("notify_payments", true),
    direction = prefs.getString("direction", "ltr") ?: "ltr",
    serverUrl = prefs.getString("server_url", "") ?: "",
)

fun saveSettings(prefs: SharedPreferences, settings: AppSettings) {
    prefs.edit()
        .putString("theme", settings.theme)
        .putBoolean("biometric_lock", settings.biometricLock)
        .putBoolean("notify_approvals", settings.notifyApprovals)
        .putBoolean("notify_payments", settings.notifyPayments)
        .putString("direction", settings.direction)
        .putString("server_url", settings.serverUrl.trim())
        .apply()
}
