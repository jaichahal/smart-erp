package com.smarterp.app

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import kotlin.math.hypot

private val Alert = Color(0xFFB91C1C)

@Composable
fun SignedInHome(session: DeviceSession.Session) {
    val kind = homeKind(session.roles, session.personas)
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text(session.name, modifier = Modifier.testTag("signed-in-name"))
        Text("Home / Approval inbox")
        Text(session.roles.joinToString(", "), modifier = Modifier.testTag("profile-roles"))
        Text(kind, modifier = Modifier.testTag("home-kind"))
        PersonaBlock(kind)
        ApprovalInbox(session)
        PhoneHome(session)
    }
}

@Composable
private fun PersonaBlock(kind: String) {
    Column {
        when (kind) {
            "cfo" -> {
                Text("Cash")
                Text("Approvals are in the inbox below.")
            }
            "finance-manager" -> {
                Text("Variances")
                Text("Pending items")
                Text("Month close")
            }
            "accountant" -> {
                Text("Queue")
                Text("Capture")
            }
            "sales" -> {
                Text("Receivables")
                Text("Aging buckets")
                Text("On-account remainder")
            }
            else -> Text("The profile roles do not select a persona home, so this is the default shell.")
        }
    }
}

private data class Card(
    val id: String,
    val docNumber: String,
    val docType: String,
    val amount: String,
    val version: Int,
    val requester: String,
    val vendorId: String? = null,
)

@Composable
private fun ApprovalInbox(session: DeviceSession.Session) {
    var cards by remember { mutableStateOf<List<Card>?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var sheet by remember { mutableStateOf<Pair<String, Card>?>(null) }
    var flags by remember { mutableStateOf(setOf<String>()) }
    LaunchedEffect(session) {
        try {
            cards = withContext(Dispatchers.IO) { loadCards(session) }
        } catch (failure: Exception) {
            cards = emptyList()
            error = failure.message
        }
    }
    Text("Approval inbox")
    when {
        cards == null -> Text("Loading the approval inbox")
        error != null -> Text(error!!, color = Alert, modifier = Modifier.testTag("inbox-error"))
        cards!!.isEmpty() -> Text("Nothing is waiting on you.")
    }
    cards.orEmpty().forEach { card ->
        ApprovalCard(card, flags.contains(card.id)) { kind -> sheet = kind to card }
    }
    sheet?.let { (kind, card) ->
        DecisionSheet(session, kind, card, onClose = { sheet = null }, onFlag = { flags = flags + card.id })
    }
}

private fun loadCards(session: DeviceSession.Session): List<Card> {
    val seen = linkedSetOf<String>()
    val cards = mutableListOf<Card>()
    for (state in listOf("needs_me", "waiting_on_others", "fyi")) {
        val (code, json) = session.exchange("GET", "/api/v1/approvals/inbox?state=$state", null)
        if (code !in 200..299) {
            val message = json.optJSONObject("error")?.optString("message").orEmpty()
            throw IllegalStateException(if (message.isBlank()) "HTTP $code" else message)
        }
        val data = json.optJSONArray("data") ?: JSONArray()
        for (index in 0 until data.length()) {
            val item = data.getJSONObject(index)
            val id = item.getString("request_id")
            if (!seen.add(id)) continue
            val amount = item.optJSONObject("amount")
            cards += Card(
                id = id,
                docNumber = item.optString("doc_number", id),
                docType = item.optString("doc_type"),
                amount = listOf(amount?.optString("amount").orEmpty(), amount?.optString("currency").orEmpty()).filter { it.isNotBlank() }.joinToString(" ").ifBlank { "—" },
                version = item.optInt("state_version", 1),
                requester = item.optJSONObject("requester")?.optString("name").orEmpty().ifBlank { "—" },
            )
        }
    }
    return cards
}

@Composable
private fun ApprovalCard(card: Card, flagged: Boolean, onOpen: (String) -> Unit) {
    Column(
        Modifier
            .fillMaxWidth()
            .heightIn(min = 44.dp)
            .testTag("approval-card")
            .padding(vertical = 8.dp)
            .clickable { onOpen("review") }
            .pointerInput(card.id) {
                awaitPointerEventScope {
                    while (true) {
                        val down = awaitFirstDown(requireUnconsumed = false)
                        var total = Offset.Zero
                        while (true) {
                            val event = awaitPointerEvent()
                            val change = event.changes.firstOrNull { it.id == down.id } ?: break
                            if (!change.pressed) break
                            total += change.position - change.previousPosition
                            if (hypot(total.x.toDouble(), total.y.toDouble()) > 12.0) change.consume()
                        }
                        val kind = sheetForDrag(total.x, total.y)
                        if (kind != null) onOpen(kind)
                    }
                }
            },
    ) {
        Text(card.docNumber)
        Text("${card.docType} ${card.amount}")
        Text("Requester ${card.requester}")
        if (flagged) Text("Flagged for review", color = Alert)
    }
}

@Composable
private fun DecisionSheet(
    session: DeviceSession.Session,
    kind: String,
    card: Card,
    onClose: () -> Unit,
    onFlag: () -> Unit,
) {
    var state by remember { mutableStateOf("loading") }
    var version by remember { mutableStateOf(card.version) }
    var message by remember { mutableStateOf("") }
    var reason by remember { mutableStateOf("") }
    var stepCode by remember { mutableStateOf("") }
    var needStepUp by remember { mutableStateOf(false) }
    var actions by remember { mutableStateOf(emptySet<String>()) }
    LaunchedEffect(card.id) {
        val detail = withContext(Dispatchers.IO) {
            session.exchange("GET", "/api/v1/approvals/${card.id}", null)
        }
        val dashboard = withContext(Dispatchers.IO) {
            session.exchange("GET", "/api/v1/purchase/dashboard", null)
        }
        val (code, json) = detail
        if (code in 200..299) {
            val data = json.optJSONObject("data")
            state = data?.optString("state").orEmpty().ifBlank { "pending" }
            version = data?.optInt("state_version", card.version) ?: card.version
        } else {
            message = json.optJSONObject("error")?.optString("message") ?: "The live approval was not returned"
        }
        if (dashboard.first in 200..299) {
            actions = readActions(dashboard.second)
        }
    }
    Column(Modifier.fillMaxWidth().background(Color.White).padding(16.dp).testTag("approval-sheet")) {
        val title = when (kind) {
            "approve" -> "Approve request"
            "reject" -> "Reject request"
            "review" -> "Review request"
            else -> "Flag for review"
        }
        Text(title)
        Text("Live state $state", modifier = Modifier.testTag("approval-state"))
        Text(card.amount)
        if (kind == "reject") {
            androidx.compose.material3.TextField(
                value = reason,
                onValueChange = { reason = it },
                label = { Text("Reason") },
                modifier = Modifier.fillMaxWidth().testTag("reject-reason"),
            )
        }
        if (kind == "approve" && needStepUp) {
            androidx.compose.material3.TextField(
                value = stepCode,
                onValueChange = { stepCode = it },
                label = { Text("Step-up code") },
                modifier = Modifier.fillMaxWidth().testTag("step-up-code"),
            )
        }
        if (message.isNotBlank()) Text(message, color = Alert)
        CommitButtons(session, kind, card, version, reason, stepCode, needStepUp, actions, onFlag) { next, stepped ->
            message = next
            if (stepped) needStepUp = true
        }
        TextButton(onClick = onClose, modifier = Modifier.heightIn(min = 44.dp)) { Text("Close") }
    }
}

@Composable
private fun CommitButtons(
    session: DeviceSession.Session,
    kind: String,
    card: Card,
    version: Int,
    reason: String,
    stepCode: String,
    needStepUp: Boolean,
    actions: Set<String>,
    onFlag: () -> Unit,
    report: (String, Boolean) -> Unit,
) {
    val scope = rememberCoroutineScope()
    Row {
        if (kind == "approve" && actions.contains("approve")) {
            Button(
                onClick = {
                    scope.launch {
                        val result = withContext(Dispatchers.IO) { submitApprove(session, card.id, version, stepCode, needStepUp) }
                        report(result.message, result.needStepUp)
                    }
                },
                modifier = Modifier.heightIn(min = 44.dp).testTag("approve-submit"),
            ) { Text("Approve") }
        }
        if (kind == "reject") {
            Button(
                onClick = {
                    scope.launch {
                        val result = withContext(Dispatchers.IO) {
                            val (code, json) = session.exchange(
                                "POST",
                                "/api/v1/approvals/${card.id}/reject",
                                JSONObject().put("reason", reason.trim()).put("state_version", version),
                            )
                            if (code in 200..299) "Recorded rejected" else json.optJSONObject("error")?.optString("message") ?: "Reject was refused"
                        }
                        report(result, false)
                    }
                },
                enabled = reason.trim().isNotEmpty(),
                modifier = Modifier.heightIn(min = 44.dp).testTag("reject-submit"),
            ) { Text("Reject") }
        }
        if (actions.contains("blacklist")) {
            Button(
                onClick = {
                    scope.launch {
                        val result = withContext(Dispatchers.IO) { submitBlacklist(session, card) }
                        report(result, false)
                    }
                },
                modifier = Modifier.heightIn(min = 44.dp).testTag("blacklist-submit"),
            ) { Text("Blacklist") }
        }
        if (kind == "flag") {
            Button(onClick = {
                onFlag()
                report("Flagged for review", false)
            }, modifier = Modifier.heightIn(min = 44.dp)) { Text("Flag for review") }
        }
    }
}

private fun readActions(json: JSONObject): Set<String> {
    val data = json.optJSONObject("data") ?: return emptySet()
    val actions = data.optJSONArray("actions") ?: return emptySet()
    return buildSet {
        for (index in 0 until actions.length()) {
            val name = actions.optString(index)
            if (name.isNotBlank()) add(name)
        }
    }
}

private fun submitBlacklist(session: DeviceSession.Session, card: Card): String {
    val vendor = card.vendorId
    if (vendor.isNullOrBlank()) return "This request has no vendor to blacklist."
    val (code, json) = session.exchange(
        "POST",
        "/api/v1/vendors/$vendor/blacklist",
        JSONObject().put("change_reason", "fraud"),
    )
    if (code in 200..299) return "Recorded blacklist"
    return json.optJSONObject("error")?.optString("message") ?: "Blacklist was refused"
}

private data class SubmitResult(val message: String, val needStepUp: Boolean)

private fun submitApprove(session: DeviceSession.Session, id: String, version: Int, stepCode: String, needStepUp: Boolean): SubmitResult {
    var token = ""
    if (needStepUp) {
        if (!Regex("^\\d{6}$").matches(stepCode)) return SubmitResult("Enter the 6-digit code from the authenticator.", true)
        val (code, json) = session.exchange("POST", "/api/v1/auth/step-up", JSONObject().put("method", "totp").put("code", stepCode))
        if (code !in 200..299) return SubmitResult(json.optJSONObject("error")?.optString("message") ?: "Step-up was not verified", true)
        token = json.optJSONObject("data")?.optString("step_up_token").orEmpty()
        if (token.isBlank()) return SubmitResult("Step-up did not return a token", true)
    }
    val body = JSONObject().put("state_version", version)
    if (token.isNotBlank()) body.put("step_up_token", token)
    val (code, json) = session.exchange("POST", "/api/v1/approvals/$id/approve", body)
    val error = json.optJSONObject("error")
    if (error?.optString("code") == "STEP_UP_REQUIRED") return SubmitResult(error.optString("message").ifBlank { "Step-up is required" }, true)
    if (code !in 200..299) return SubmitResult(error?.optString("message") ?: "Approve was refused", false)
    return SubmitResult("Recorded ${json.optJSONObject("data")?.optString("state") ?: "updated"}", false)
}
