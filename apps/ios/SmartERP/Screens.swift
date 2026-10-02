import SwiftUI

enum Persona {
    private static let tabs: [String: [String]] = [
        "Stakeholder": ["Brief", "Approvals", "Activity", "Reports", "Profile"],
        "Sales Agent": ["My Day", "Stock", "Orders", "Activity", "Profile"],
        "Collection Agent": ["Receivables", "Receipts", "Customers", "Activity", "Profile"],
        "Driver": ["Trip", "Activity", "Profile"],
        "Accountant": ["Queue", "Capture", "Approvals", "Activity", "Profile"],
        "Credit Controller": ["Queue", "Approvals", "Activity", "Profile"],
        "Stock Counter": ["Stock", "Activity", "Profile"],
        "Production Supervisor": ["Stock", "Activity", "Profile"],
        "Auditor": ["Activity", "Reports", "Profile"],
        "System Manager": ["Admin", "Activity", "Profile"],
    ]

    static func title(for account: Account) -> String {
        let labels = account.personas + account.roles
        if let known = labels.first(where: { tabs[$0] != nil }) {
            return known
        }
        if labels.contains(where: { ["CFO", "Partner", "CTO"].contains($0) }) {
            return "Stakeholder"
        }
        return account.personas.first ?? account.roles.first ?? "Unknown"
    }

    static func tabs(for title: String) -> [String] {
        tabs[title] ?? ["Profile"]
    }

    static func canGateVendor(_ account: Account) -> Bool {
        (account.roles + account.personas).contains { $0 == "CFO" || $0 == "Partner" }
    }
}

private struct VendorRow: Identifiable {
    let id: String
    let name: String
    let status: String
    let active: Int
    let past: Int
    let skuLines: [String]
    let approvalID: String
}

private struct BestPrice {
    let id: String
    let number: String
    let amount: String
    let currency: String
    let window: String
}

struct PhoneHome: View {
    let account: Account
    @State private var screen = "home"

    var body: some View {
        let title = Persona.title(for: account)
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .accessibilityIdentifier("persona-home")
            ForEach(Persona.tabs(for: title) + ["Vendor dashboard", "Sales order", "Collection receipt", "Purchase list"], id: \.self) { label in
                Button(label) {
                    switch label {
                    case "Vendor dashboard": screen = "vendor"
                    case "Sales order": screen = "sales"
                    case "Collection receipt": screen = "collection"
                    case "Purchase list": screen = "purchases"
                    default: screen = "home"
                    }
                }
                .frame(minWidth: 44, minHeight: 44)
                .accessibilityIdentifier(label)
            }
            switch screen {
            case "vendor": VendorDashboard(account: account)
            case "sales": SalesOrderForm(account: account)
            case "collection": CollectionReceiptForm(account: account)
            case "purchases": PurchaseList(account: account)
            default: Text("\(title) home")
            }
        }
    }
}

struct VendorDashboard: View {
    let account: Account
    @State private var sku = ""
    @State private var error = ""
    @State private var status = ""
    @State private var vendors: [VendorRow] = []
    @State private var best: BestPrice?
    @State private var blocked = ""
    @State private var sheet: VendorRow?
    @State private var reason = ""
    @State private var actionError = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Vendor dashboard")
            TextField("Raw-material SKU", text: $sku)
                .accessibilityIdentifier("sku-filter")
                .frame(minHeight: 44)
            Button("Apply SKU filter") {
                Task { await load() }
            }
            .frame(minWidth: 44, minHeight: 44)
            .accessibilityIdentifier("apply-sku-filter")
            if !error.isEmpty { Text(error).accessibilityIdentifier("vendor-dashboard-error") }
            if !status.isEmpty { Text(status).accessibilityIdentifier("vendor-dashboard-status") }
            if status == "loaded" {
                VStack(alignment: .leading) {
                    if vendors.isEmpty { Text("No vendors approved for this SKU") }
                    ForEach(vendors) { row in
                        vendorLine(row)
                    }
                }
                .accessibilityIdentifier("invoice-counts")
                VStack(alignment: .leading) {
                    if let best {
                        Text(best.id).accessibilityIdentifier("best-price-source-id")
                        Text(best.number).accessibilityIdentifier("best-price-source-number")
                        Text(best.amount).accessibilityIdentifier("best-price-unit-price")
                        Text(best.currency).accessibilityIdentifier("best-price-currency")
                        Text(best.window).accessibilityIdentifier("best-price-window")
                    } else {
                        Text("No best price").accessibilityIdentifier("best-price-empty")
                    }
                }
                .accessibilityIdentifier("best-price")
                VStack(alignment: .leading) {
                    Text("Blocked")
                    Text(blocked.isEmpty ? "No blocked vendors" : blocked)
                }
                .accessibilityIdentifier("blocked-group")
            }
            if let sheet {
                reviewSheet(sheet)
            }
        }
    }

    private func vendorLine(_ row: VendorRow) -> some View {
        VStack(alignment: .leading) {
            Text(row.name)
            Text(row.status)
            Text("Active invoices \(row.active)").accessibilityIdentifier("active-invoice-count")
            Text("Past invoices \(row.past)").accessibilityIdentifier("past-invoice-count")
            ForEach(row.skuLines, id: \.self) { Text($0) }
        }
        .frame(minHeight: 44, alignment: .leading)
        .contentShape(Rectangle())
        .accessibilityIdentifier("vendor-row")
        .gesture(
            DragGesture(minimumDistance: 24).onEnded { value in
                if value.translation.width < -48 {
                    reason = ""
                    actionError = ""
                    self.sheet = row
                }
            }
        )
    }

    private func reviewSheet(_ row: VendorRow) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Review")
            if Persona.canGateVendor(account) {
                Button("Approve") { Task { await commit(row, "approve") } }.frame(minHeight: 44)
                Button("Blacklist") { Task { await commit(row, "blacklist") } }.frame(minHeight: 44)
            }
            TextField("Reject reason", text: $reason)
                .accessibilityIdentifier("reject-reason")
                .frame(minHeight: 44)
            Button("Reject") { Task { await commit(row, "reject") } }
                .disabled(reason.trimmingCharacters(in: .whitespaces).isEmpty)
                .frame(minHeight: 44)
            Button("Close") { sheet = nil }.frame(minHeight: 44)
            if !actionError.isEmpty { Text(actionError) }
        }
        .accessibilityIdentifier("approval-sheet")
    }

    private func load() async {
        error = ""
        status = ""
        let path = "/api/v1/vendors/dashboard?sku=\(sku.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? sku)"
        do {
            let payload = try await DeviceSession.authorized(account, method: "GET", path: path)
            guard let data = payload as? [String: Any], let raw = data["vendors"] as? [[String: Any]] else {
                error = "GET \(path) failed unexpected vendor dashboard payload"
                return
            }
            vendors = raw.map { row in
                let skus = row["skus"] as? [[String: Any]] ?? []
                return VendorRow(
                    id: row["id"] as? String ?? UUID().uuidString,
                    name: row["name"] as? String ?? "",
                    status: row["status"] as? String ?? "",
                    active: row["active_invoice_count"] as? Int ?? 0,
                    past: row["past_invoice_count"] as? Int ?? 0,
                    skuLines: skus.map { sku in
                        "\(sku["sku"] ?? "") active \(sku["active_invoice_count"] ?? 0) past \(sku["past_invoice_count"] ?? 0)"
                    },
                    approvalID: row["approval_id"] as? String ?? ""
                )
            }
            if let bestPrice = data["best_price"] as? [String: Any], let source = bestPrice["source"] as? [String: Any] {
                let price = source["unit_price"] as? [String: Any]
                best = BestPrice(
                    id: source["id"] as? String ?? "",
                    number: source["number"] as? String ?? "",
                    amount: price?["amount"] as? String ?? "",
                    currency: price?["currency"] as? String ?? source["currency"] as? String ?? "",
                    window: "\(source["effective_from"] ?? "") to \(source["effective_to"] ?? "")"
                )
            } else {
                best = nil
            }
            let blockedRows = data["blocked"] as? [[String: Any]] ?? []
            blocked = blockedRows.map { "\($0["name"] ?? ""): \($0["reason"] ?? "")" }.joined(separator: "\n")
            status = "loaded"
        } catch {
            self.error = "GET \(path) failed \(error)"
        }
    }

    private func commit(_ row: VendorRow, _ kind: String) async {
        if kind == "reject" && reason.trimmingCharacters(in: .whitespaces).isEmpty { return }
        let path: String
        if kind == "reject" && !row.approvalID.isEmpty {
            path = "/api/v1/approvals/\(row.approvalID)/reject"
        } else {
            path = "/api/v1/vendors/\(row.id)/\(kind)"
        }
        do {
            _ = try await DeviceSession.authorized(account, method: "POST", path: path, body: ["reason": reason, "state_version": 0])
            sheet = nil
        } catch {
            actionError = "POST \(path) failed \(error)"
        }
    }
}

struct SalesOrderForm: View {
    let account: Account
    @State private var customer = ""
    @State private var sku = ""
    @State private var quantity = "1"
    @State private var price = ""
    @State private var error = ""
    @State private var number = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Sales order")
            TextField("Customer", text: $customer).accessibilityIdentifier("customer").frame(minHeight: 44)
            TextField("SKU", text: $sku).accessibilityIdentifier("order-sku").frame(minHeight: 44)
            TextField("Quantity", text: $quantity).accessibilityIdentifier("quantity").frame(minHeight: 44)
            TextField("Unit price", text: $price).accessibilityIdentifier("unit-price").frame(minHeight: 44)
            Button("Submit order") { Task { await submit() } }
                .frame(minWidth: 44, minHeight: 44)
                .accessibilityIdentifier("submit-order")
            if !error.isEmpty { Text(error).accessibilityIdentifier("sales-order-error") }
            if !number.isEmpty { Text(number).accessibilityIdentifier("sales-order-number") }
        }
    }

    private func submit() async {
        error = ""
        number = ""
        let path = "/api/v1/sales-orders"
        let body: [String: Any] = [
            "customer_id": customer,
            "lines": [[
                "sku": sku,
                "quantity": quantity,
                "uom": "ea",
                "unit_price": ["amount": price, "currency": "AED"],
            ]],
        ]
        do {
            let payload = try await DeviceSession.authorized(account, method: "POST", path: path, body: body)
            number = documentNumber(payload)
            if number.isEmpty { error = "POST \(path) failed unexpected sales order payload" }
        } catch {
            self.error = "POST \(path) failed \(error)"
        }
    }
}

struct CollectionReceiptForm: View {
    let account: Account
    @State private var amount = ""
    @State private var error = ""
    @State private var number = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Collection receipt")
            TextField("Amount", text: $amount).accessibilityIdentifier("amount").frame(minHeight: 44)
            Button("Record receipt") { Task { await submit() } }
                .frame(minWidth: 44, minHeight: 44)
                .accessibilityIdentifier("record-receipt")
            if !error.isEmpty { Text(error).accessibilityIdentifier("collection-receipt-error") }
            if !number.isEmpty { Text(number).accessibilityIdentifier("collection-receipt-number") }
        }
    }

    private func submit() async {
        error = ""
        number = ""
        let path = "/api/v1/receipts"
        let money: [String: String] = ["amount": amount, "currency": "AED"]
        let body: [String: Any] = [
            "method": "cash",
            "amount": money,
            "allocations": [] as [Any],
            "on_account": money,
        ]
        do {
            let payload = try await DeviceSession.authorized(account, method: "POST", path: path, body: body)
            number = documentNumber(payload)
            if number.isEmpty { error = "POST \(path) failed unexpected receipt payload" }
        } catch {
            self.error = "POST \(path) failed \(error)"
        }
    }
}

struct PurchaseList: View {
    let account: Account
    @State private var error = ""
    @State private var status = ""
    @State private var rows: [String] = []

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Purchase list")
            if !error.isEmpty { Text(error).accessibilityIdentifier("purchase-list-error") }
            if !status.isEmpty { Text(status).accessibilityIdentifier("purchase-list-status") }
            if status == "loaded" {
                VStack(alignment: .leading) {
                    if rows.isEmpty { Text("No purchase documents") }
                    ForEach(rows, id: \.self) { Text($0).frame(minHeight: 44).accessibilityIdentifier("purchase-row") }
                }
                .accessibilityIdentifier("purchase-list")
            }
        }
        .task { await load() }
    }

    private func load() async {
        let path = "/api/v1/lpos"
        do {
            let payload = try await DeviceSession.authorized(account, method: "GET", path: path)
            guard let found = purchaseRows(payload) else {
                self.error = "GET \(path) failed unexpected purchase list payload"
                return
            }
            rows = found
            status = "loaded"
        } catch {
            self.error = "GET \(path) failed \(error)"
        }
    }
}

private func documentNumber(_ payload: Any) -> String {
    guard let data = payload as? [String: Any] else { return "" }
    if let number = data["number"] as? String, !number.isEmpty { return number }
    if let number = data["doc_number"] as? String, !number.isEmpty { return number }
    return data["id"] as? String ?? ""
}

private func purchaseRows(_ payload: Any) -> [String]? {
    let array: [[String: Any]]
    if let rows = payload as? [[String: Any]] {
        array = rows
    } else if let data = payload as? [String: Any], let items = data["items"] as? [[String: Any]] {
        array = items
    } else if let data = payload as? [String: Any], let items = data["lpos"] as? [[String: Any]] {
        array = items
    } else {
        return nil
    }
    return array.map { ($0["number"] as? String).flatMap { $0.isEmpty ? nil : $0 } ?? ($0["id"] as? String ?? "") }
}
