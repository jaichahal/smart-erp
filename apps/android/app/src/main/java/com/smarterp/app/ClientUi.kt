package com.smarterp.app

import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.defaultMinSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject

private val tabs = mapOf(
    "Stakeholder" to listOf("Brief", "Approvals", "Activity", "Reports", "Profile"),
    "Sales Agent" to listOf("My Day", "Stock", "Orders", "Activity", "Profile"),
    "Collection Agent" to listOf("Receivables", "Receipts", "Customers", "Activity", "Profile"),
    "Driver" to listOf("Trip", "Activity", "Profile"),
    "Accountant" to listOf("Queue", "Capture", "Approvals", "Activity", "Profile"),
    "Credit Controller" to listOf("Queue", "Approvals", "Activity", "Profile"),
    "Stock Counter" to listOf("Stock", "Activity", "Profile"),
    "Production Supervisor" to listOf("Stock", "Activity", "Profile"),
    "Auditor" to listOf("Activity", "Reports", "Profile"),
    "System Manager" to listOf("Admin", "Activity", "Profile"),
)

private val stakeholderTitles = setOf("CFO", "Partner", "CTO")

fun personaTitle(profile: Profile): String {
    val labels = profile.personas + profile.roles
    labels.firstOrNull { tabs.containsKey(it) }?.let { return it }
    if (labels.any { stakeholderTitles.contains(it) }) {
        return "Stakeholder"
    }
    return profile.personas.firstOrNull() ?: profile.roles.firstOrNull() ?: "Unknown"
}

fun personaTabs(title: String): List<String> = tabs[title] ?: listOf("Profile")

fun canGateVendor(profile: Profile): Boolean =
    (profile.roles + profile.personas).any { it == "CFO" || it == "Partner" }

private fun Modifier.tapTarget(): Modifier = defaultMinSize(minWidth = 44.dp, minHeight = 44.dp)

@Composable
fun PhoneHome(profile: Profile) {
    val title = personaTitle(profile)
    var screen by remember { mutableStateOf("home") }
    Column {
        Text(title, modifier = Modifier.testTag("persona-home"))
        (personaTabs(title) + listOf("Vendor dashboard", "Sales order", "Collection receipt", "Purchase list")).forEach { label ->
            Button(
                onClick = {
                    screen = when (label) {
                        "Vendor dashboard" -> "vendor"
                        "Sales order" -> "sales"
                        "Collection receipt" -> "collection"
                        "Purchase list" -> "purchases"
                        else -> "home"
                    }
                },
                modifier = Modifier.tapTarget().testTag(label),
            ) {
                Text(label)
            }
        }
        when (screen) {
            "vendor" -> VendorDashboard(profile)
            "sales" -> SalesOrder(profile)
            "collection" -> CollectionReceipt(profile)
            "purchases" -> PurchaseList(profile)
            else -> Text("$title home")
        }
    }
}

private data class VendorRow(
    val id: String,
    val name: String,
    val status: String,
    val active: Int,
    val past: Int,
    val skus: List<String>,
    val approvalId: String,
)

private data class BestPrice(
    val id: String,
    val number: String,
    val amount: String,
    val currency: String,
    val window: String,
)

@Composable
fun VendorDashboard(profile: Profile) {
    var sku by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }
    var vendors by remember { mutableStateOf<List<VendorRow>>(emptyList()) }
    var best by remember { mutableStateOf<BestPrice?>(null) }
    var blocked by remember { mutableStateOf("") }
    var sheet by remember { mutableStateOf<VendorRow?>(null) }
    var reason by remember { mutableStateOf("") }
    var actionError by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    val gate = canGateVendor(profile)
    Column {
        Text("Vendor dashboard")
        TextField(value = sku, onValueChange = { sku = it }, label = { Text("Raw-material SKU") }, modifier = Modifier.testTag("sku-filter").tapTarget())
        Button(
            onClick = {
                scope.launch {
                    error = ""
                    status = ""
                    val path = "/api/v1/vendors/dashboard?sku=" + java.net.URLEncoder.encode(sku, "UTF-8")
                    try {
                        val json = withContext(Dispatchers.IO) { DeviceSession.authorized(profile, "GET", path) }
                        val parsed = parseDashboard(json)
                        vendors = parsed.first
                        best = parsed.second
                        blocked = parsed.third
                        status = "loaded"
                    } catch (failure: Exception) {
                        error = "GET $path failed ${failure.message}"
                    }
                }
            },
            modifier = Modifier.tapTarget().testTag("apply-sku-filter"),
        ) { Text("Apply SKU filter") }
        if (error.isNotEmpty()) Text(error, modifier = Modifier.testTag("vendor-dashboard-error"))
        if (status.isNotEmpty()) Text(status, modifier = Modifier.testTag("vendor-dashboard-status"))
        if (status == "loaded") {
            Column(Modifier.testTag("invoice-counts")) {
                if (vendors.isEmpty()) {
                    Text("No vendors approved for this SKU")
                }
                vendors.forEach { row ->
                    VendorLine(row) {
                        reason = ""
                        actionError = ""
                        sheet = row
                    }
                }
            }
            Column(Modifier.testTag("best-price")) {
                val price = best
                if (price == null) {
                    Text("No best price", modifier = Modifier.testTag("best-price-empty"))
                } else {
                    Text(price.id, modifier = Modifier.testTag("best-price-source-id"))
                    Text(price.number, modifier = Modifier.testTag("best-price-source-number"))
                    Text(price.amount, modifier = Modifier.testTag("best-price-unit-price"))
                    Text(price.currency, modifier = Modifier.testTag("best-price-currency"))
                    Text(price.window, modifier = Modifier.testTag("best-price-window"))
                }
            }
            Column(Modifier.testTag("blocked-group")) {
                Text("Blocked")
                Text(if (blocked.isEmpty()) "No blocked vendors" else blocked)
            }
        }
        val open = sheet
        if (open != null) {
            ReviewSheet(
                gate = gate,
                reason = reason,
                actionError = actionError,
                onReason = { reason = it },
                onClose = { sheet = null },
                onApprove = {
                    scope.launch { actionError = postAction(profile, "/api/v1/vendors/${open.id}/approve", reason) { sheet = null } }
                },
                onBlacklist = {
                    scope.launch { actionError = postAction(profile, "/api/v1/vendors/${open.id}/blacklist", reason) { sheet = null } }
                },
                onReject = {
                    if (reason.isNotBlank()) {
                        val path = if (open.approvalId.isBlank()) "/api/v1/vendors/${open.id}/reject" else "/api/v1/approvals/${open.approvalId}/reject"
                        scope.launch { actionError = postAction(profile, path, reason) { sheet = null } }
                    }
                },
            )
        }
    }
}

@Composable
private fun VendorLine(row: VendorRow, onOpen: () -> Unit) {
    Column(
        Modifier
            .tapTarget()
            .testTag("vendor-row")
            .pointerInput(row.id) {
                var drag = 0f
                detectHorizontalDragGestures(
                    onDragStart = { drag = 0f },
                    onHorizontalDrag = { _, amount -> drag += amount },
                    onDragCancel = { drag = 0f },
                    onDragEnd = {
                        if (drag < -48f) onOpen()
                        drag = 0f
                    },
                )
            },
    ) {
        Text(row.name)
        Text(row.status)
        Text("Active invoices ${row.active}", modifier = Modifier.testTag("active-invoice-count"))
        Text("Past invoices ${row.past}", modifier = Modifier.testTag("past-invoice-count"))
        row.skus.forEach { Text(it) }
    }
}

@Composable
private fun ReviewSheet(
    gate: Boolean,
    reason: String,
    actionError: String,
    onReason: (String) -> Unit,
    onClose: () -> Unit,
    onApprove: () -> Unit,
    onBlacklist: () -> Unit,
    onReject: () -> Unit,
) {
    Column(Modifier.testTag("approval-sheet")) {
        Text("Review")
        if (gate) {
            Button(onClick = onApprove, modifier = Modifier.tapTarget()) { Text("Approve") }
            Button(onClick = onBlacklist, modifier = Modifier.tapTarget()) { Text("Blacklist") }
        }
        TextField(value = reason, onValueChange = onReason, label = { Text("Reject reason") }, modifier = Modifier.tapTarget())
        Button(onClick = onReject, enabled = reason.isNotBlank(), modifier = Modifier.tapTarget()) { Text("Reject") }
        Button(onClick = onClose, modifier = Modifier.tapTarget()) { Text("Close") }
        if (actionError.isNotEmpty()) Text(actionError)
    }
}

@Composable
fun SalesOrder(profile: Profile) {
    var customer by remember { mutableStateOf("") }
    var sku by remember { mutableStateOf("") }
    var quantity by remember { mutableStateOf("1") }
    var price by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var number by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    Column {
        Text("Sales order")
        TextField(value = customer, onValueChange = { customer = it }, label = { Text("Customer") }, modifier = Modifier.testTag("customer").tapTarget())
        TextField(value = sku, onValueChange = { sku = it }, label = { Text("SKU") }, modifier = Modifier.testTag("order-sku").tapTarget())
        TextField(value = quantity, onValueChange = { quantity = it }, label = { Text("Quantity") }, modifier = Modifier.testTag("quantity").tapTarget())
        TextField(value = price, onValueChange = { price = it }, label = { Text("Unit price") }, modifier = Modifier.testTag("unit-price").tapTarget())
        Button(
            onClick = {
                scope.launch {
                    error = ""
                    number = ""
                    val path = "/api/v1/sales-orders"
                    val body = JSONObject()
                        .put("customer_id", customer)
                        .put("lines", JSONArray().put(JSONObject()
                            .put("sku", sku)
                            .put("quantity", quantity)
                            .put("uom", "ea")
                            .put("unit_price", JSONObject().put("amount", price).put("currency", "AED"))))
                    try {
                        val json = withContext(Dispatchers.IO) { DeviceSession.authorized(profile, "POST", path, body) }
                        val found = documentNumber(json)
                        if (found.isBlank()) error = "POST $path failed unexpected sales order payload" else number = found
                    } catch (failure: Exception) {
                        error = "POST $path failed ${failure.message}"
                    }
                }
            },
            modifier = Modifier.tapTarget().testTag("submit-order"),
        ) { Text("Submit order") }
        if (error.isNotEmpty()) Text(error, modifier = Modifier.testTag("sales-order-error"))
        if (number.isNotEmpty()) Text(number, modifier = Modifier.testTag("sales-order-number"))
    }
}

@Composable
fun CollectionReceipt(profile: Profile) {
    var amount by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var number by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    Column {
        Text("Collection receipt")
        TextField(value = amount, onValueChange = { amount = it }, label = { Text("Amount") }, modifier = Modifier.testTag("amount").tapTarget())
        Button(
            onClick = {
                scope.launch {
                    error = ""
                    number = ""
                    val path = "/api/v1/receipts"
                    val money = JSONObject().put("amount", amount).put("currency", "AED")
                    val body = JSONObject()
                        .put("method", "cash")
                        .put("amount", money)
                        .put("allocations", JSONArray())
                        .put("on_account", money)
                    try {
                        val json = withContext(Dispatchers.IO) { DeviceSession.authorized(profile, "POST", path, body) }
                        val found = documentNumber(json)
                        if (found.isBlank()) error = "POST $path failed unexpected receipt payload" else number = found
                    } catch (failure: Exception) {
                        error = "POST $path failed ${failure.message}"
                    }
                }
            },
            modifier = Modifier.tapTarget().testTag("record-receipt"),
        ) { Text("Record receipt") }
        if (error.isNotEmpty()) Text(error, modifier = Modifier.testTag("collection-receipt-error"))
        if (number.isNotEmpty()) Text(number, modifier = Modifier.testTag("collection-receipt-number"))
    }
}

@Composable
fun PurchaseList(profile: Profile) {
    var error by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }
    var rows by remember { mutableStateOf<List<String>>(emptyList()) }
    androidx.compose.runtime.LaunchedEffect(profile.accessToken) {
        val path = "/api/v1/lpos"
        try {
            val json = withContext(Dispatchers.IO) { DeviceSession.authorized(profile, "GET", path) }
            rows = purchaseRows(json)
            status = "loaded"
        } catch (failure: Exception) {
            error = "GET $path failed ${failure.message}"
        }
    }
    Column {
        Text("Purchase list")
        if (error.isNotEmpty()) Text(error, modifier = Modifier.testTag("purchase-list-error"))
        if (status.isNotEmpty()) Text(status, modifier = Modifier.testTag("purchase-list-status"))
        if (status == "loaded") {
            Column(Modifier.testTag("purchase-list")) {
                if (rows.isEmpty()) Text("No purchase documents") else rows.forEach { Text(it, modifier = Modifier.testTag("purchase-row").tapTarget()) }
            }
        }
    }
}

private suspend fun postAction(profile: Profile, path: String, reason: String, onDone: () -> Unit): String {
    return try {
        withContext(Dispatchers.IO) {
            DeviceSession.authorized(profile, "POST", path, JSONObject().put("reason", reason).put("state_version", 0))
        }
        onDone()
        ""
    } catch (failure: Exception) {
        "POST $path failed ${failure.message}"
    }
}

private fun parseDashboard(json: JSONObject): Triple<List<VendorRow>, BestPrice?, String> {
    val data = json.getJSONObject("data")
    val rawVendors = data.optJSONArray("vendors") ?: throw IllegalStateException("unexpected vendor dashboard payload")
    val vendors = List(rawVendors.length()) { index ->
        val row = rawVendors.getJSONObject(index)
        val skus = row.optJSONArray("skus")
        val lines = if (skus == null) emptyList() else List(skus.length()) { skuIndex ->
            val sku = skus.getJSONObject(skuIndex)
            "${sku.optString("sku")} active ${sku.optInt("active_invoice_count")} past ${sku.optInt("past_invoice_count")}"
        }
        VendorRow(
            id = row.optString("id"),
            name = row.optString("name"),
            status = row.optString("status"),
            active = row.optInt("active_invoice_count"),
            past = row.optInt("past_invoice_count"),
            skus = lines,
            approvalId = row.optString("approval_id"),
        )
    }
    val best = data.optJSONObject("best_price")?.optJSONObject("source")?.let { source ->
        val price = source.optJSONObject("unit_price")
        BestPrice(
            id = source.optString("id"),
            number = source.optString("number"),
            amount = price?.optString("amount") ?: "",
            currency = price?.optString("currency") ?: source.optString("currency"),
            window = source.optString("effective_from") + " to " + source.optString("effective_to"),
        )
    }
    val blockedArray = data.optJSONArray("blocked")
    val blocked = if (blockedArray == null) "" else List(blockedArray.length()) { index ->
        val row = blockedArray.getJSONObject(index)
        row.optString("name") + ": " + row.optString("reason")
    }.joinToString("\n")
    return Triple(vendors, best, blocked)
}

private fun documentNumber(json: JSONObject): String {
    val data = json.optJSONObject("data") ?: return ""
    return data.optString("number").ifBlank { data.optString("doc_number") }.ifBlank { data.optString("id") }
}

private fun purchaseRows(json: JSONObject): List<String> {
    val data = json.opt("data")
    val array = when (data) {
        is JSONArray -> data
        is JSONObject -> data.optJSONArray("items") ?: data.optJSONArray("lpos")
        else -> null
    } ?: throw IllegalStateException("unexpected purchase list payload")
    return List(array.length()) { index ->
        val row = array.optJSONObject(index)
        row?.optString("number").orEmpty().ifBlank { row?.optString("id").orEmpty() }
    }
}
