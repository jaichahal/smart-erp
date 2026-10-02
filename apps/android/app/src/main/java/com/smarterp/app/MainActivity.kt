package com.smarterp.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import kotlinx.coroutines.launch
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

// Branched from integration/e2e @ 8d797bdc930eb4b28e498f4645f87684e4d2e92f.
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val baseUrl = BuildConfig.API_BASE_URL
        setContent {
            MaterialTheme {
                Column {
                    BaselineHealthStatus(baseUrl)
                    SignInScreen(baseUrl)
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
            else -> Text("Checking the baseline API")
        }
    }
}

@Composable
fun SignInScreen(baseUrl: String) {
    var loginName by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    var signedInName by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    Column {
        TextField(
            value = loginName,
            onValueChange = { loginName = it },
            label = { Text("Login name") },
            modifier = Modifier.testTag("login-name"),
        )
        TextField(
            value = password,
            onValueChange = { password = it },
            label = { Text("Password") },
            modifier = Modifier.testTag("password"),
        )
        Button(
            onClick = {
                error = null
                scope.launch {
                    try {
                        signedInName = withContext(Dispatchers.IO) {
                            DeviceSession.signIn(baseUrl, loginName, password)
                        }
                    } catch (failure: Exception) {
                        error = failure.message
                    }
                }
            },
            modifier = Modifier.testTag("sign-in"),
        ) {
            Text("Sign in")
        }
        signedInName?.let { Text(it, modifier = Modifier.testTag("signed-in-name")) }
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
