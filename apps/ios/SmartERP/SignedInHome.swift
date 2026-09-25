import SwiftUI

struct SignedInHome: View {
    let session: APISession
    @State private var cards: [ApprovalCard] = []
    @State private var loaded = false
    @State private var errorLine = ""
    @State private var sheet: SheetRequest?
    @State private var flags: Set<String> = []

    private var kind: String { homeKind(roles: session.roles, personas: session.personas) }

    var body: some View {
        ScrollViewReader { proxy in
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text(session.name).accessibilityIdentifier("signed-in-name")
                Text("Home / Approval inbox").foregroundStyle(.secondary)
                Text(session.roles.joined(separator: ", ")).accessibilityIdentifier("profile-roles")
                Text(kind).accessibilityIdentifier("home-kind")
                PersonaHome(kind: kind)
                Text("Approval inbox").font(.title2)
                if !loaded { Text("Loading the approval inbox") }
                if !errorLine.isEmpty { Text(errorLine).foregroundStyle(.red).accessibilityIdentifier("inbox-error") }
                if loaded && cards.isEmpty && errorLine.isEmpty { Text("Nothing is waiting on you.") }
                ForEach(cards) { card in
                    ApprovalCardView(card: card, flagged: flags.contains(card.id)) { opened in
                        sheet = SheetRequest(kind: opened, card: card)
                    }
                    .id(card.id)
                }
            }
            .padding()
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .onChange(of: cards.first?.id) { _, id in
            guard let pinned = UserDefaults.standard.string(forKey: "approvalDoc"),
                  cards.first?.docNumber == pinned,
                  let id else { return }
            proxy.scrollTo(id, anchor: .center)
        }
        .task { await load() }
        }
        .sheet(item: $sheet) { request in
            DecisionSheet(session: session, request: request) {
                flags.insert(request.card.id)
            }
        }
    }

    private func load() async {
        if CommandLine.arguments.contains("-prepareApproval") {
            await prepareApproval()
        }
        do {
            var merged: [ApprovalCard] = []
            var seen = Set<String>()
            for state in ["needs_me", "waiting_on_others", "fyi"] {
                let (status, body) = try await session.exchange("GET", path: "/api/v1/approvals/inbox?state=\(state)", body: nil)
                guard (200..<300).contains(status) else {
                    errorLine = (body["error"] as? [String: Any])?["message"] as? String ?? "Approval inbox was not returned"
                    loaded = true
                    return
                }
                for item in (body["data"] as? [[String: Any]]) ?? [] {
                    guard let card = ApprovalCard(json: item), seen.insert(card.id).inserted else { continue }
                    merged.append(card)
                }
            }
            if let pinned = UserDefaults.standard.string(forKey: "approvalDoc"), !pinned.isEmpty {
                let match = merged.filter { $0.docNumber == pinned }
                if !match.isEmpty { merged = match }
            }
            cards = merged
            errorLine = ""
        } catch {
            errorLine = String(describing: error)
        }
        loaded = true
    }

    private func prepareApproval() async {
        let doc = UserDefaults.standard.string(forKey: "approvalDoc").flatMap { $0.isEmpty ? nil : $0 } ?? "UI-IOS"
        let roles = session.roles.isEmpty ? ["Sales Agent"] : session.roles
        _ = try? await session.exchange("POST", path: "/api/v1/approvals/actors", body: [
            "id": session.userId, "name": session.name, "department": "sales", "roles": roles,
        ])
        _ = try? await session.exchange("POST", path: "/api/v1/approvals/matrix", body: [
            "doc_type": "sales_invoice",
            "threshold_amount": "1000.00",
            "currency": "AED",
            "below_roles": roles,
            "first_roles": ["finance manager"],
            "final_role": "cfo",
            "above_mode": "first_then_final",
            "vote_n": 0,
            "requires_step_up_above": true,
        ])
        _ = try? await session.exchange("POST", path: "/api/v1/approvals/requests", body: [
            "doc_id": doc,
            "doc_type": "sales_invoice",
            "doc_number": doc,
            "party": "Al Noor",
            "amount": "25.00",
            "currency": "AED",
            "snapshot": ["doc_number": doc],
        ])
    }
}

private struct PersonaHome: View {
    let kind: String

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            switch kind {
            case "cfo":
                Text("Cash").font(.title3)
                Text("Approvals are in the inbox below.")
            case "finance-manager":
                Text("Variances").font(.title3)
                Text("Pending items").font(.title3)
                Text("Month close").font(.title3)
            case "accountant":
                Text("Queue").font(.title3)
                Text("Capture").font(.title3)
            case "sales":
                Text("Receivables").font(.title3)
                Text("Aging buckets").font(.title3)
                Text("On-account remainder")
            default:
                Text("The profile roles do not select a persona home, so this is the default shell.")
            }
        }
    }
}

struct ApprovalCard: Identifiable {
    let id: String
    let docNumber: String
    let docType: String
    let amount: String
    let version: Int
    let requester: String

    init?(json: [String: Any]) {
        guard let id = json["request_id"] as? String else { return nil }
        self.id = id
        docNumber = (json["doc_number"] as? String).flatMap { $0.isEmpty ? nil : $0 } ?? id
        docType = json["doc_type"] as? String ?? ""
        let money = json["amount"] as? [String: Any]
        let figure = [money?["amount"] as? String, money?["currency"] as? String].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " ")
        amount = figure.isEmpty ? "—" : figure
        version = json["state_version"] as? Int ?? 1
        requester = (json["requester"] as? [String: Any])?["name"] as? String ?? "—"
    }
}

private struct SheetRequest: Identifiable {
    let kind: String
    let card: ApprovalCard
    var id: String { kind + card.id }
}

private struct ApprovalCardView: View {
    let card: ApprovalCard
    let flagged: Bool
    let onOpen: (String) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(card.docNumber).font(.headline).accessibilityHidden(true)
            Text("\(card.docType) \(card.amount)")
            Text("Requester \(card.requester)")
            if flagged { Text("Flagged for review").foregroundStyle(.red) }
        }
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .padding(8)
        .contentShape(Rectangle())
        .background(Color.white.opacity(0.001))
        .allowsHitTesting(false)
        .overlay(CardPan(label: card.docNumber) { translation in
            if let kind = sheetForDrag(dx: translation.width, dy: translation.height) {
                onOpen(kind)
            }
        })
    }
}

private struct DecisionSheet: View {
    let session: APISession
    let request: SheetRequest
    let onFlag: () -> Void
    @State private var state = "loading"
    @State private var version: Int
    @State private var reason = ""
    @State private var stepCode = ""
    @State private var needStepUp = false
    @State private var message = ""
    @Environment(\.dismiss) private var dismiss

    init(session: APISession, request: SheetRequest, onFlag: @escaping () -> Void) {
        self.session = session
        self.request = request
        self.onFlag = onFlag
        _version = State(initialValue: request.card.version)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(title).font(.title2)
            Text("Live state \(state)").accessibilityIdentifier("approval-state")
            Text(request.card.amount)
            if request.kind == "reject" {
                TextField("Reason", text: $reason, axis: .vertical)
                    .textFieldStyle(.roundedBorder)
                    .accessibilityIdentifier("reject-reason")
            }
            if request.kind == "approve" && needStepUp {
                TextField("Step-up code", text: $stepCode)
                    .textFieldStyle(.roundedBorder)
                    .keyboardType(.numberPad)
                    .accessibilityIdentifier("step-up-code")
            }
            if !message.isEmpty { Text(message).foregroundStyle(.red) }
            if request.kind == "approve" {
                Button("Approve") { Task { await approve() } }
                    .frame(minHeight: 44)
                    .accessibilityIdentifier("approve-submit")
            }
            if request.kind == "reject" {
                Button("Reject") { Task { await reject() } }
                    .disabled(reason.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    .frame(minHeight: 44)
            }
            if request.kind == "flag" {
                Button("Flag for review") {
                    onFlag()
                    message = "Flagged for review"
                }
                .frame(minHeight: 44)
            }
            Button("Close") { dismiss() }.frame(minHeight: 44)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(.ultraThinMaterial)
        .task { await load() }
    }

    private var title: String {
        switch request.kind {
        case "approve": return "Approve request"
        case "reject": return "Reject request"
        default: return "Flag for review"
        }
    }

    private func load() async {
        guard let (status, body) = try? await session.exchange("GET", path: "/api/v1/approvals/\(request.card.id)", body: nil) else { return }
        guard (200..<300).contains(status), let data = body["data"] as? [String: Any] else {
            message = (body["error"] as? [String: Any])?["message"] as? String ?? "The live approval was not returned"
            return
        }
        state = data["state"] as? String ?? "pending"
        version = data["state_version"] as? Int ?? version
    }

    private func approve() async {
        var token = ""
        if needStepUp {
            guard stepCode.range(of: #"^\d{6}$"#, options: .regularExpression) != nil else {
                message = "Enter the 6-digit code from the authenticator."
                return
            }
            guard let (status, body) = try? await session.exchange("POST", path: "/api/v1/auth/step-up", body: ["method": "totp", "code": stepCode]),
                  (200..<300).contains(status),
                  let data = body["data"] as? [String: Any],
                  let issued = data["step_up_token"] as? String, !issued.isEmpty else {
                message = "Step-up was not verified"
                return
            }
            token = issued
        }
        var body: [String: Any] = ["state_version": version]
        if !token.isEmpty { body["step_up_token"] = token }
        guard let (status, result) = try? await session.exchange("POST", path: "/api/v1/approvals/\(request.card.id)/approve", body: body) else {
            message = "Approve was refused"
            return
        }
        if let error = result["error"] as? [String: Any], error["code"] as? String == "STEP_UP_REQUIRED" {
            needStepUp = true
            message = error["message"] as? String ?? "Step-up is required"
            return
        }
        if !(200..<300).contains(status) {
            message = (result["error"] as? [String: Any])?["message"] as? String ?? "Approve was refused"
            return
        }
        let recorded = (result["data"] as? [String: Any])?["state"] as? String ?? "updated"
        message = "Recorded \(recorded)"
    }

    private func reject() async {
        guard let (status, result) = try? await session.exchange("POST", path: "/api/v1/approvals/\(request.card.id)/reject", body: [
            "reason": reason.trimmingCharacters(in: .whitespacesAndNewlines),
            "state_version": version,
        ]) else { return }
        if (200..<300).contains(status) {
            message = "Recorded rejected"
        } else {
            message = (result["error"] as? [String: Any])?["message"] as? String ?? "Reject was refused"
        }
    }
}

private struct CardPan: UIViewRepresentable {
    var label: String
    var onEnd: (CGSize) -> Void

    func makeUIView(context: Context) -> PanHost {
        let view = PanHost()
        view.isAccessibilityElement = true
        view.accessibilityTraits = .staticText
        view.accessibilityLabel = label
        view.accessibilityIdentifier = "approval-card"
        view.onEnd = onEnd
        return view
    }

    func updateUIView(_ uiView: PanHost, context: Context) {
        uiView.accessibilityLabel = label
        uiView.onEnd = onEnd
        uiView.bindScrollIfNeeded()
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: PanHost, context: Context) -> CGSize? {
        CGSize(width: proposal.width ?? 320, height: proposal.height ?? 88)
    }
}

final class PanHost: UIView, UIGestureRecognizerDelegate {
    var onEnd: ((CGSize) -> Void)?
    private var start = CGPoint.zero
    private var boundScroll = false
    private let pan = UIPanGestureRecognizer()

    override init(frame: CGRect) {
        super.init(frame: frame)
        backgroundColor = .clear
        isUserInteractionEnabled = true
        pan.addTarget(self, action: #selector(panned(_:)))
        pan.delegate = self
        addGestureRecognizer(pan)
    }

    required init?(coder: NSCoder) { nil }

    override func layoutSubviews() {
        super.layoutSubviews()
        bindScrollIfNeeded()
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        bindScrollIfNeeded()
    }

    func bindScrollIfNeeded() {
        guard !boundScroll else { return }
        var view: UIView? = superview
        while let current = view {
            if let scroll = current as? UIScrollView {
                scroll.panGestureRecognizer.require(toFail: pan)
                boundScroll = true
                return
            }
            view = current.superview
        }
    }

    @objc private func panned(_ pan: UIPanGestureRecognizer) {
        let location = pan.location(in: self)
        if pan.state == .began { start = location }
        guard pan.state == .ended || pan.state == .cancelled else { return }
        onEnd?(CGSize(width: location.x - start.x, height: location.y - start.y))
    }

    func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldBeRequiredToFailBy otherGestureRecognizer: UIGestureRecognizer) -> Bool {
        true
    }
}
