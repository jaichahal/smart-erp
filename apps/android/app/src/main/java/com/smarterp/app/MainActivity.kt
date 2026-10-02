package com.smarterp.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TextField
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import kotlinx.coroutines.launch
import androidx.compose.foundation.background
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

// Branched from integration/e2e @ 8d797bdc930eb4b28e498f4645f87684e4d2e92f.
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val prefs = getSharedPreferences("smarterp-settings", MODE_PRIVATE)
        setContent {
            var settings by remember { mutableStateOf(loadSettings(prefs)) }
            val dark = prefersDark(settings.theme, isSystemInDarkTheme())
            val baseUrl = activeBaseUrl(settings.serverUrl, currentApiBaseUrl())
            fun update(next: AppSettings) {
                saveSettings(prefs, next)
                settings = next
            }
            MaterialTheme(colorScheme = if (dark) darkScheme() else lightScheme()) {
                CompositionLocalProvider(
                    LocalLayoutDirection provides if (settings.direction == "rtl") LayoutDirection.Rtl else LayoutDirection.Ltr,
                ) {
                    Column {
                        Text(if (dark) "dark" else "light", modifier = Modifier.testTag("theme-applied"))
                        BaselineHealthStatus(baseUrl)
                        SignInScreen(baseUrl, settings, ::update)
                    }
                }
            }
        }
    }
}

@Composable
fun BaselineHealthStatus(baseUrl: String) {
    var status by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(baseUrl) {
        try {
            status = withContext(Dispatchers.IO) { fetchBaselineHealthStatus(baseUrl) }
        } catch (failure: Exception) {
            error = failure.message
        }
    }
    Column {
        Text("Smart ERP")
        when {
            status != null -> Text(status!!, modifier = Modifier.testTag("health-status"))
            error != null -> Text(error!!, modifier = Modifier.testTag("health-error"))
            else -> Box(
                Modifier
                    .fillMaxWidth()
                    .height(44.dp)
                    .background(MaterialTheme.colorScheme.surface)
                    .testTag("skeleton"),
            )
        }
    }
}

@Composable
fun SignInScreen(baseUrl: String, settings: AppSettings, onSettings: (AppSettings) -> Unit) {
    var loginName by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    var showPassword by remember { mutableStateOf(false) }
    var session by remember { mutableStateOf<DeviceSession.Session?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var mode by remember { mutableStateOf("phone") }
    val scope = rememberCoroutineScope()
    session?.let {
        SignedInHome(it, settings, onSettings)
        return
    }
    if (mode == "phone") {
        PhoneOnboarding(baseUrl) { mode = "email" }
        return
    }
    Column {
        TextField(
            value = loginName,
            onValueChange = { loginName = it },
            label = { Text("Login name") },
            modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp).testTag("login-name"),
        )
        TextField(
            value = password,
            onValueChange = { password = it },
            label = { Text("Password") },
            visualTransformation = if (showPassword) VisualTransformation.None else PasswordVisualTransformation(),
            modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp).testTag("password"),
        )
        TextButton(
            onClick = { showPassword = !showPassword },
            modifier = Modifier.heightIn(min = 44.dp).testTag("toggle-password"),
        ) {
            Text(if (showPassword) "Hide password" else "Show password")
        }
        Button(
            onClick = {
                error = null
                scope.launch {
                    try {
                        session = withContext(Dispatchers.IO) {
                            DeviceSession.open(baseUrl, loginName, password)
                        }
                    } catch (failure: Exception) {
                        error = failure.message
                    }
                }
            },
            modifier = Modifier.heightIn(min = 44.dp).testTag("sign-in"),
        ) {
            Text("Sign in")
        }
        error?.let { Text(it, modifier = Modifier.testTag("sign-in-error")) }
    }
}

private fun fetchBaselineHealthStatus(baseUrl: String): String {
    val url = baseUrl.trimEnd('/') + "/health"
    val conn = (URL(url).openConnection() as HttpURLConnection).apply {
        requestMethod = "GET"
        connectTimeout = 5_000
        readTimeout = 5_000
    }
    return try {
        val code = conn.responseCode
        val stream = if (code in 200..299) conn.inputStream else conn.errorStream
        val body = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
        if (code != 200) {
            throw IllegalStateException("baseline API is not reachable at $url: HTTP $code $body")
        }
        val status = JSONObject(body).getJSONObject("data").getString("status")
        if (status.isBlank()) {
            throw IllegalStateException("baseline API at $url returned an empty health status")
        }
        status
    } catch (error: IllegalStateException) {
        throw error
    } catch (error: Exception) {
        throw IllegalStateException(
            "baseline API is not reachable at $url: ${error.javaClass.simpleName}: ${error.message}",
            error,
        )
    } finally {
        conn.disconnect()
    }
}
