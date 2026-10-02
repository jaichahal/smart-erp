package com.smarterp.app

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.material3.Button
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject

@Composable
fun SettingsScreen(session: DeviceSession.Session, settings: AppSettings, onChange: (AppSettings) -> Unit) {
    var draftUrl by remember(settings.serverUrl) { mutableStateOf(settings.serverUrl) }
    var biometricDetail by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    Column(Modifier.testTag("settings-screen")) {
        Text("Settings")
        Text("Theme")
        Button(onClick = { onChange(settings.copy(theme = "system")) }, modifier = Modifier.heightIn(min = 44.dp).testTag("theme-system")) { Text("System") }
        Button(onClick = { onChange(settings.copy(theme = "light")) }, modifier = Modifier.heightIn(min = 44.dp).testTag("theme-light")) { Text("Light") }
        Button(onClick = { onChange(settings.copy(theme = "dark")) }, modifier = Modifier.heightIn(min = 44.dp).testTag("theme-dark")) { Text("Black") }
        Text(settings.theme, modifier = Modifier.testTag("theme-value"))
        Text("Biometric lock")
        Switch(
            checked = settings.biometricLock,
            onCheckedChange = { on ->
                onChange(settings.copy(biometricLock = on))
                if (!on) {
                    biometricDetail = ""
                    return@Switch
                }
                scope.launch {
                    val (code, json) = withContext(Dispatchers.IO) {
                        session.exchange("POST", "/api/v1/auth/step-up", JSONObject().put("method", "biometric").put("code", ""))
                    }
                    val token = json.optJSONObject("data")?.optString("step_up_token").orEmpty()
                    biometricDetail = if (code in 200..299 && token.isNotBlank()) {
                        "Step-up accepted"
                    } else {
                        json.optJSONObject("error")?.optString("message").orEmpty().ifBlank { "HTTP $code" }
                    }
                }
            },
            modifier = Modifier.heightIn(min = 44.dp).testTag("biometric-lock"),
        )
        if (settings.biometricLock) {
            Text("Confirm with biometrics", modifier = Modifier.testTag("biometric-prompt"))
        }
        if (biometricDetail.isNotBlank()) Text(biometricDetail, modifier = Modifier.testTag("biometric-error"))
        Text("Approval notifications")
        Switch(
            checked = settings.notifyApprovals,
            onCheckedChange = { onChange(settings.copy(notifyApprovals = it)) },
            modifier = Modifier.heightIn(min = 44.dp).testTag("notify-approvals"),
        )
        Text("Payment notifications")
        Switch(
            checked = settings.notifyPayments,
            onCheckedChange = { onChange(settings.copy(notifyPayments = it)) },
            modifier = Modifier.heightIn(min = 44.dp).testTag("notify-payments"),
        )
        Text(if (settings.direction == "rtl") "الاتجاه من اليمين" else "Left to right", modifier = Modifier.testTag("direction-preview"))
        Button(onClick = { onChange(settings.copy(direction = "ltr")) }, modifier = Modifier.heightIn(min = 44.dp).testTag("direction-ltr")) { Text("Left to right") }
        Button(onClick = { onChange(settings.copy(direction = "rtl")) }, modifier = Modifier.heightIn(min = 44.dp).testTag("direction-rtl")) { Text("Right to left") }
        Text(settings.direction, modifier = Modifier.testTag("direction-value"))
        Text("Dev server")
        TextField(
            value = draftUrl,
            onValueChange = { draftUrl = it },
            label = { Text("Server URL") },
            modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp).testTag("server-url"),
        )
        Button(
            onClick = { onChange(settings.copy(serverUrl = draftUrl.trim())) },
            modifier = Modifier.heightIn(min = 44.dp).testTag("save-server"),
        ) { Text("Save server") }
        Text(
            "This phone has no offline copy. Screens read the live API.",
            modifier = Modifier.testTag("sync-status"),
        )
        Text(BuildConfig.VERSION_NAME, modifier = Modifier.testTag("app-version"))
    }
}
