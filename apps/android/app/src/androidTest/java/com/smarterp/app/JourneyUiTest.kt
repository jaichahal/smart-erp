package com.smarterp.app

import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
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
    @Test fun collectionAndAging() = expect("Aging buckets")
    @Test fun bankMatch() = expect("Matched bank line")
    @Test fun purchaseLpoThroughPayment() = expect("LPO payment released")
    @Test fun stockCount() = expect("Stock count posted")
    @Test fun approvalInbox() = expect("Approval inbox")
    @Test fun notifications() = expect("Notifications")

    private fun expect(heading: String) {
        compose.onNodeWithText(heading).assertExists()
    }
}
