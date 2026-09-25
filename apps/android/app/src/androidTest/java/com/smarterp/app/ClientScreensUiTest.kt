package com.smarterp.app

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeLeft
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.fail
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.net.HttpURLConnection
import java.net.URL

@RunWith(AndroidJUnit4::class)
class ClientScreensUiTest {
    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    @Test
    fun personaHomeFollowsMe() {
        signIn()
        compose.onNodeWithTag("persona-home").assertTextEquals("Sales Agent")
        requireTag("My Day")
    }

    @Test
    fun salesAgentDoesNotSeeApproveOrBlacklist() {
        signIn()
        compose.onNodeWithTag("Vendor dashboard").performScrollTo().performClick()
        absentText("Approve")
        absentText("Blacklist")
    }

    @Test
    fun vendorDashboardCallsLiveApi() {
        signIn()
        compose.onNodeWithTag("Vendor dashboard").performScrollTo().performClick()
        compose.onNodeWithTag("sku-filter").performScrollTo().performTextInput("RM-TEST")
        compose.onNodeWithTag("apply-sku-filter").performScrollTo().performClick()
        awaitTag("vendor-dashboard-status", "vendor-dashboard-error")
        failIfTag("vendor-dashboard-error", "vendor dashboard")
        requireTag("invoice-counts")
        requireTag("best-price")
        requireTag("blocked-group")
    }

    @Test
    fun swipeOpensSheetWithoutApproving() {
        signIn()
        compose.onNodeWithTag("Vendor dashboard").performScrollTo().performClick()
        compose.onNodeWithTag("sku-filter").performScrollTo().performTextInput("RM-TEST")
        compose.onNodeWithTag("apply-sku-filter").performScrollTo().performClick()
        awaitTag("vendor-dashboard-status", "vendor-dashboard-error")
        failIfTag("vendor-dashboard-error", "vendor dashboard swipe")
        compose.onNodeWithTag("vendor-row").performScrollTo().performTouchInput { swipeLeft() }
        requireTag("approval-sheet")
        absentText("Approved")
        absentText("Approve")
    }

    @Test
    fun salesOrderCallsLiveApi() {
        signIn()
        compose.onNodeWithTag("Sales order").performScrollTo().performClick()
        compose.onNodeWithTag("customer").performScrollTo().performTextInput("00000000-0000-4000-8000-000000000002")
        compose.onNodeWithTag("order-sku").performScrollTo().performTextInput("FG-1")
        compose.onNodeWithTag("quantity").performScrollTo().performTextInput("1")
        compose.onNodeWithTag("unit-price").performScrollTo().performTextInput("10.00")
        compose.onNodeWithTag("submit-order").performScrollTo().performClick()
        awaitTag("sales-order-number", "sales-order-error")
        failIfTag("sales-order-error", "sales order")
    }

    @Test
    fun collectionReceiptCallsLiveApi() {
        signIn()
        compose.onNodeWithTag("Collection receipt").performScrollTo().performClick()
        compose.onNodeWithTag("amount").performScrollTo().performTextInput("25.00")
        compose.onNodeWithTag("record-receipt").performScrollTo().performClick()
        awaitTag("collection-receipt-number", "collection-receipt-error")
        failIfTag("collection-receipt-error", "collection receipt")
    }

    @Test
    fun purchaseListCallsLiveApi() {
        signIn()
        compose.onNodeWithTag("Purchase list").performScrollTo().performClick()
        awaitTag("purchase-list-status", "purchase-list-error")
        failIfTag("purchase-list-error", "purchase list")
        requireTag("purchase-list")
    }

    private fun signIn() {
        val base = currentApiBaseUrl()
        val health = get("$base/health")
        if (health.code != 200 || !health.body.contains("\"ready\"")) {
            fail("baseline API is not reachable at $base/health: HTTP ${health.code} ${health.body}")
        }
        compose.onNodeWithTag("login-name").performTextInput("admin@dev.localhost")
        compose.onNodeWithTag("password").performTextInput("Admin1234!")
        compose.onNodeWithTag("sign-in").performClick()
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodesWithTag("signed-in-name").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("signed-in-name").assertTextEquals("Dev Admin")
    }

    private fun awaitTag(ok: String, error: String) {
        compose.waitUntil(timeoutMillis = 20_000) {
            compose.onAllNodesWithTag(ok).fetchSemanticsNodes().isNotEmpty() ||
                compose.onAllNodesWithTag(error).fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun failIfTag(tag: String, label: String) {
        if (compose.onAllNodesWithTag(tag).fetchSemanticsNodes().isNotEmpty()) {
            fail("$label: ${textOf(tag)}")
        }
    }

    private fun requireTag(tag: String) {
        compose.onNodeWithTag(tag).performScrollTo()
        if (compose.onAllNodesWithTag(tag).fetchSemanticsNodes().isEmpty()) {
            fail("missing $tag")
        }
    }

    private fun absentText(label: String) {
        if (compose.onAllNodesWithText(label).fetchSemanticsNodes().isNotEmpty()) {
            fail("unexpected $label")
        }
    }

    private fun textOf(tag: String): String {
        val text = compose.onNodeWithTag(tag).fetchSemanticsNode().config.getOrNull(SemanticsProperties.Text)
        return text?.joinToString { it.text }.orEmpty()
    }
}

private data class ScreenProbe(val code: Int, val body: String)

private fun get(url: String): ScreenProbe {
    val conn = (URL(url).openConnection() as HttpURLConnection).apply {
        requestMethod = "GET"
        connectTimeout = 8_000
        readTimeout = 8_000
    }
    return try {
        val code = conn.responseCode
        val stream = if (code in 200..299) conn.inputStream else conn.errorStream
        ScreenProbe(code, stream?.bufferedReader()?.use { it.readText() }.orEmpty())
    } finally {
        conn.disconnect()
    }
}
