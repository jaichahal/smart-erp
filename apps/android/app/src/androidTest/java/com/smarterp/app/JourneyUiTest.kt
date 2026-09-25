package com.smarterp.app

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Journeys the phone does not perform yet. Each assertion names the outcome
 * from docs/spec/08-acceptance-tests.md and stays red until that screen exists.
 */
@RunWith(AndroidJUnit4::class)
class JourneyUiTest {
    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    @Test fun salesOrderThroughDelivery() = expect("Delivery note registered")
    @Test fun collectionAndAging() {
        signIn()
        compose.onNodeWithText("Aging buckets").assertExists()
        compose.onNodeWithText("On-account remainder").assertExists()
    }
    @Test fun bankMatch() = expect("Matched bank line")
    @Test fun purchaseLpoThroughPayment() = expect("LPO payment released")
    @Test fun stockCount() = expect("Stock count posted")
    @Test fun approvalInbox() {
        val api = DeviceSession.open(currentApiBaseUrl(), "admin@dev.localhost", "Admin1234!")
        val doc = "UI-AND-INBOX-${System.currentTimeMillis()}"
        val id = seedWaiting(api, doc)
        val before = approvalState(api, id)
        signIn()
        compose.onNodeWithText("Approval inbox").assertExists()
        compose.onNode(hasTestTag("home-kind") and hasText(expectedHome(api.roles, api.personas), substring = false)).assertExists()
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodes(hasText(doc)).fetchSemanticsNodes().isNotEmpty()
        }
        card(doc).performScrollTo().performClick()
        compose.onNodeWithText("Review request").assertExists()
        compose.onNodeWithText("Approve", substring = false).assertDoesNotExist()
        compose.onNodeWithText("Blacklist", substring = false).assertDoesNotExist()
        assertEquals(before, approvalState(api, id))
    }
    @Test fun notifications() = expect("Notifications")

    @Test
    fun swipeDoesNotChangeApprovalState() {
        val api = DeviceSession.open(currentApiBaseUrl(), "admin@dev.localhost", "Admin1234!")
        val doc = "UI-AND-${System.currentTimeMillis()}"
        val id = seedWaiting(api, doc)
        val before = approvalState(api, id)
        signIn()
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodesWithTag("signed-in-name").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodes(androidx.compose.ui.test.hasText(doc)).fetchSemanticsNodes().isNotEmpty()
        }
        swipe(doc, Offset(220f, 0f))
        compose.onNodeWithText("Approve request").assertExists()
        assertEquals(before, approvalState(api, id))
        compose.onNodeWithText("Close").performClick()
        swipe(doc, Offset(-180f, 0f))
        compose.onNodeWithText("Reject request").assertExists()
        assertEquals(before, approvalState(api, id))
    }

    private fun card(doc: String) = compose.onNode(
        hasTestTag("approval-card") and hasAnyDescendant(hasText(doc)),
        useUnmergedTree = true,
    )

    private fun swipe(doc: String, delta: Offset) {
        val node = card(doc)
        node.performScrollTo()
        node.performTouchInput {
            if (delta.y > kotlin.math.abs(delta.x)) {
                down(topCenter)
                moveTo(bottomCenter)
            } else if (delta.x < 0) {
                down(centerRight)
                moveTo(centerLeft)
            } else {
                down(centerLeft)
                moveTo(centerRight)
            }
            up()
        }
    }

    private fun signIn() {
        compose.onNodeWithTag("login-name").performTextInput("admin@dev.localhost")
        compose.onNodeWithTag("password").performTextInput("Admin1234!")
        compose.onNodeWithTag("sign-in").performClick()
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodesWithTag("signed-in-name").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun expect(heading: String) {
        compose.onNodeWithText(heading).assertExists()
    }
}

private fun expectedHome(roles: List<String>, personas: List<String>): String {
    val labels = (roles + personas).map { it.trim().lowercase() }.filter { it.isNotEmpty() }
    fun has(needle: String) = labels.any { it.contains(needle) }
    return when {
        has("cfo") || has("partner") -> "cfo"
        has("finance manager") || has("finance_manager") -> "finance-manager"
        has("accountant") -> "accountant"
        has("sales") || has("collection") -> "sales"
        else -> "default"
    }
}

private fun seedWaiting(api: DeviceSession.Session, doc: String): String {
    val roles = if (api.roles.isEmpty()) org.json.JSONArray().put("Sales Agent") else org.json.JSONArray().apply {
        api.roles.forEach { put(it) }
    }
    val actor = api.post("/api/v1/approvals/actors", JSONObject()
        .put("id", api.userId)
        .put("name", api.name)
        .put("department", "sales")
        .put("roles", roles))
    if (!actor.has("data") && actor.has("error")) error(actor.toString())
    val matrix = api.post("/api/v1/approvals/matrix", JSONObject()
        .put("doc_type", "sales_invoice")
        .put("threshold_amount", "1000.00")
        .put("currency", "AED")
        .put("below_roles", roles)
        .put("first_roles", org.json.JSONArray().put("finance manager"))
        .put("final_role", "cfo")
        .put("above_mode", "first_then_final")
        .put("vote_n", 0)
        .put("requires_step_up_above", true))
    if (matrix.has("error") && !matrix.has("data")) error(matrix.toString())
    val submitted = api.post("/api/v1/approvals/requests", JSONObject()
        .put("doc_id", doc)
        .put("doc_type", "sales_invoice")
        .put("doc_number", doc)
        .put("party", "Al Noor")
        .put("amount", "25.00")
        .put("currency", "AED")
        .put("snapshot", JSONObject().put("doc_number", doc)))
    return submitted.getJSONObject("data").getString("request_id")
}

private fun approvalState(api: DeviceSession.Session, id: String): String {
    return api.get("/api/v1/approvals/$id").getJSONObject("data").getString("state")
}
