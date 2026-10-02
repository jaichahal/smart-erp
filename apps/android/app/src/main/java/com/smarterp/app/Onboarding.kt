package com.smarterp.app

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.autofill.ContentType
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentType
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private val countries = listOf("+971", "+91", "+44", "+1")

@Composable
fun PhoneOnboarding(baseUrl: String, onWorkEmail: () -> Unit) {
    var step by remember { mutableStateOf("phone") }
    var country by remember { mutableStateOf("+971") }
    var phone by remember { mutableStateOf("") }
    var code by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var verified by remember { mutableStateOf(false) }
    var name by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    Column {
        when (step) {
            "phone" -> {
                Text("1 of 3", modifier = Modifier.testTag("onboarding-progress"))
                Text("What is your mobile number?", style = MaterialTheme.typography.headlineSmall)
                Row {
                    countries.forEach { item ->
                        Button(
                            onClick = { country = item },
                            modifier = Modifier.heightIn(min = 44.dp).testTag(if (item == country) "country-code" else "country-$item"),
                        ) { Text(item) }
                    }
                }
                TextField(
                    value = phone,
                    onValueChange = { phone = it.filter { ch -> ch.isDigit() } },
                    label = { Text("Phone number") },
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Phone),
                    modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp).testTag("phone-number"),
                )
                Button(
                    onClick = { step = "code" },
                    enabled = phone.length >= 7,
                    modifier = Modifier.heightIn(min = 44.dp).testTag("phone-continue"),
                ) { Text("Continue") }
                TextButton(
                    onClick = onWorkEmail,
                    modifier = Modifier.heightIn(min = 44.dp).testTag("use-work-email"),
                ) { Text("Use work email") }
            }
            "code" -> {
                Text("2 of 3", modifier = Modifier.testTag("onboarding-progress"))
                Text("Enter the code sent to $country $phone", style = MaterialTheme.typography.headlineSmall)
                TextField(
                    value = code,
                    onValueChange = { code = it.filter { ch -> ch.isDigit() }.take(6) },
                    label = { Text("Code") },
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                    modifier = Modifier
                        .fillMaxWidth()
                        .heightIn(min = 44.dp)
                        .semantics { contentType = ContentType.SmsOtpCode }
                        .testTag("otp-code"),
                )
                Button(
                    onClick = {
                        error = ""
                        scope.launch {
                            val result = withContext(Dispatchers.IO) {
                                DeviceSession.verifyPhoneCode(baseUrl, "$country$phone", code)
                            }
                            verified = result.verified
                            if (result.verified) {
                                step = "name"
                            } else {
                                error = result.detail
                            }
                        }
                    },
                    enabled = code.length >= 4,
                    modifier = Modifier.heightIn(min = 44.dp).testTag("otp-verify"),
                ) { Text("Verify code") }
                if (error.isNotBlank()) Text(error, modifier = Modifier.testTag("otp-error"))
            }
            "name" -> {
                Text("3 of 3", modifier = Modifier.testTag("onboarding-progress"))
                if (!verified) {
                    Text("The code was not verified.", modifier = Modifier.testTag("otp-error"))
                } else {
                    Text("What should we call you?", style = MaterialTheme.typography.headlineSmall)
                    TextField(
                        value = name,
                        onValueChange = { name = it },
                        label = { Text("Display name") },
                        modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp).testTag("profile-name"),
                    )
                    TextButton(
                        onClick = { step = "biometric" },
                        modifier = Modifier.heightIn(min = 44.dp).testTag("profile-skip"),
                    ) { Text("Skip") }
                }
            }
            else -> {
                Text("Confirm with biometrics", modifier = Modifier.testTag("biometric-prompt"))
                Text("A later step-up still goes to the session API. This screen does not invent a token.")
            }
        }
    }
}
