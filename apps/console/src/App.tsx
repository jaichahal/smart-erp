import { FormEvent, PointerEvent, useEffect, useRef, useState } from "react";
import { DragSheet, homeKind, HomeKind, sheetForDrag } from "./home";
import { ApiError, apiSend, Profile, signIn } from "./session";

type Money = { amount?: string; currency?: string };
type Actor = { id?: string; name?: string };
type ApprovalCard = {
  request_id: string;
  doc_number?: string;
  doc_type?: string;
  party?: string;
  amount?: Money;
  requester?: Actor;
  state_version: number;
  fraud_hints?: string[];
  waiting_since?: string;
};
type ApprovalDetail = ApprovalCard & {
  state?: string;
  snapshot_hash?: string;
  snapshot?: Record<string, unknown>;
};
type Sheet = { kind: DragSheet; card: ApprovalCard };

const nav = ["Work", "Documents", "Money", "Stock", "Reports", "Admin"];

export function App() {
  const [loginName, setLoginName] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [profile, setProfile] = useState<Profile | null>(null);
  const [error, setError] = useState("");

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      setProfile(await signIn(loginName, password));
    } catch (cause) {
      setProfile(null);
      setError(cause instanceof Error ? cause.message : "Sign-in failed");
    }
  }

  return (
    <main>
      <h1>Smart ERP</h1>
      {profile ? (
        <Shell profile={profile} />
      ) : (
        <form className="sign-in" onSubmit={onSubmit}>
          <label>
            Login name
            <input value={loginName} onChange={(event) => setLoginName(event.target.value)} autoComplete="username" />
          </label>
          <label>
            Password
            <input
              type={showPassword ? "text" : "password"}
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              autoComplete="current-password"
            />
          </label>
          <button type="button" onClick={() => setShowPassword((value) => !value)} aria-pressed={showPassword}>
            {showPassword ? "Hide password" : "Show password"}
          </button>
          <button type="submit">Sign in</button>
          {error ? <p role="alert">{error}</p> : null}
        </form>
      )}
    </main>
  );
}

function Shell({ profile }: { profile: Profile }) {
  const [section, setSection] = useState("Work");
  const kind = homeKind(profile.roles, profile.personas);
  return (
    <div className="shell">
      <nav className="rail" aria-label="Console">
        {nav.map((item) => (
          <button key={item} type="button" aria-current={section === item ? "page" : undefined} onClick={() => setSection(item)}>
            {item}
          </button>
        ))}
      </nav>
      <section className="work">
        <p data-testid="signed-in-name">{profile.name}</p>
        <p className="crumbs">Home / {section === "Work" ? "Approval inbox" : section}</p>
        {section === "Work" ? <Work profile={profile} kind={kind} /> : <EmptyModule title={section} sentence="This module is not in this slice." />}
      </section>
    </div>
  );
}

function Work({ profile, kind }: { profile: Profile; kind: HomeKind }) {
  return (
    <>
      <p data-testid="profile-roles">{profile.roles.join(", ")}</p>
      <p data-testid="profile-personas">{profile.personas.join(", ")}</p>
      <p data-testid="home-kind">{kind}</p>
      <PersonaHome kind={kind} />
      <ApprovalInbox token={profile.accessToken} />
    </>
  );
}

function PersonaHome({ kind }: { kind: HomeKind }) {
  if (kind === "cfo") {
    return (
      <article className="module">
        <h2>Cash</h2>
        <p>Available cash is shown when the snapshot endpoint returns it.</p>
        <p className="positive">Approvals are in the inbox below.</p>
      </article>
    );
  }
  if (kind === "finance-manager") {
    return (
      <article className="module">
        <h2>Variances</h2>
        <h2>Pending items</h2>
        <h2>Month close</h2>
        <p>Figures stay blank until the period snapshot is returned.</p>
      </article>
    );
  }
  if (kind === "accountant") {
    return (
      <article className="module">
        <h2>Queue</h2>
        <h2>Capture</h2>
        <p>Drafts and capture stay on this queue. The console rail is the rest of the work.</p>
      </article>
    );
  }
  if (kind === "sales") {
    return (
      <article className="module">
        <h2>Receivables</h2>
        <h2>Aging buckets</h2>
        <p>On-account remainder</p>
        <p>No open receivables were returned for this profile.</p>
      </article>
    );
  }
  return (
    <article className="module">
      <h2>Today</h2>
      <p>The profile roles do not select a persona home, so this is the default shell.</p>
    </article>
  );
}

function EmptyModule({ title, sentence }: { title: string; sentence: string }) {
  return (
    <article className="module">
      <h2>{title}</h2>
      <p>{sentence}</p>
    </article>
  );
}

function ApprovalInbox({ token }: { token: string }) {
  const [cards, setCards] = useState<ApprovalCard[] | null>(null);
  const [error, setError] = useState("");
  const [offline, setOffline] = useState(!navigator.onLine);
  const [sheet, setSheet] = useState<Sheet | null>(null);
  const [flags, setFlags] = useState<Record<string, boolean>>({});

  useEffect(() => {
    const on = () => setOffline(false);
    const off = () => setOffline(true);
    window.addEventListener("online", on);
    window.addEventListener("offline", off);
    return () => {
      window.removeEventListener("online", on);
      window.removeEventListener("offline", off);
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const states = ["needs_me", "waiting_on_others", "fyi"];
        const lists = await Promise.all(states.map(async (state) => {
          const result = await apiSend(token, "GET", `/api/v1/approvals/inbox?state=${state}`);
          if (result.status < 200 || result.status >= 300) {
            throw new ApiError(result.error?.code || "REQUEST_FAILED", result.error?.message || "Approval inbox was not returned");
          }
          return (result.data as ApprovalCard[]) ?? [];
        }));
        if (!cancelled) {
          const seen = new Set<string>();
          const merged: ApprovalCard[] = [];
          for (const list of lists) {
            for (const card of list) {
              if (seen.has(card.request_id)) continue;
              seen.add(card.request_id);
              merged.push(card);
            }
          }
          setCards(merged);
          setError("");
        }
      } catch (cause) {
        if (!cancelled) {
          setCards([]);
          setError(cause instanceof Error ? cause.message : "Approval inbox was not returned");
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [token]);

  return (
    <article className="module">
      <h2>Approval inbox</h2>
      {offline ? <p className="banner alert" role="status">Offline since the browser lost its connection.</p> : null}
      {cards === null ? <p>Loading the approval inbox</p> : null}
      {error ? <p role="alert">{error}</p> : null}
      {cards && cards.length === 0 && !error ? <p>Nothing is waiting on you.</p> : null}
      {cards?.map((card) => (
        <ApprovalCardView
          key={card.request_id}
          card={card}
          flagged={Boolean(flags[card.request_id])}
          onOpen={(kind) => setSheet({ kind, card })}
        />
      ))}
      {sheet ? (
        <DecisionSheet
          token={token}
          sheet={sheet}
          onClose={() => setSheet(null)}
          onFlagged={(id) => setFlags((current) => ({ ...current, [id]: true }))}
        />
      ) : null}
    </article>
  );
}

function ApprovalCardView({ card, flagged, onOpen }: { card: ApprovalCard; flagged: boolean; onOpen: (kind: DragSheet) => void }) {
  const origin = useRef<{ x: number; y: number } | null>(null);

  function onPointerDown(event: PointerEvent<HTMLElement>) {
    origin.current = { x: event.clientX, y: event.clientY };
    event.currentTarget.setPointerCapture(event.pointerId);
  }

  function onPointerUp(event: PointerEvent<HTMLElement>) {
    if (!origin.current) return;
    const kind = sheetForDrag(event.clientX - origin.current.x, event.clientY - origin.current.y);
    origin.current = null;
    if (kind) onOpen(kind);
  }

  const money = card.amount?.amount ? `${card.amount.amount} ${card.amount.currency ?? ""}`.trim() : "—";
  return (
    <article
      className="card"
      data-testid="approval-card"
      onPointerDown={onPointerDown}
      onPointerUp={onPointerUp}
    >
      <h3>{card.doc_number || card.request_id}</h3>
      <p>{card.doc_type} {card.party ? `· ${card.party}` : ""}</p>
      <p>{money}</p>
      <p>Requester {card.requester?.name || "—"}</p>
      {flagged ? <p className="alert">Flagged for review</p> : null}
    </article>
  );
}

function DecisionSheet({ token, sheet, onClose, onFlagged }: { token: string; sheet: Sheet; onClose: () => void; onFlagged: (id: string) => void }) {
  const [detail, setDetail] = useState<ApprovalDetail | null>(null);
  const [reason, setReason] = useState("");
  const [stepCode, setStepCode] = useState("");
  const [needStepUp, setNeedStepUp] = useState(false);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const result = await apiSend(token, "GET", `/api/v1/approvals/${sheet.card.request_id}`);
      if (cancelled) return;
      if (result.status >= 200 && result.status < 300) setDetail(result.data as ApprovalDetail);
      else setMessage(result.error?.message || "The live approval was not returned");
    })();
    return () => {
      cancelled = true;
    };
  }, [token, sheet.card.request_id]);

  async function approve() {
    setBusy(true);
    setMessage("");
    try {
      let stepToken = "";
      if (needStepUp) {
        if (!/^\d{6}$/.test(stepCode)) {
          setMessage("Enter the 6-digit code from the authenticator.");
          return;
        }
        const stepped = await apiSend(token, "POST", "/api/v1/auth/step-up", { method: "totp", code: stepCode });
        if (stepped.status < 200 || stepped.status >= 300) {
          setMessage(stepped.error?.message || "Step-up was not verified");
          return;
        }
        stepToken = String((stepped.data as { step_up_token?: string })?.step_up_token || "");
        if (!stepToken) {
          setMessage("Step-up did not return a token");
          return;
        }
      }
      const result = await apiSend(token, "POST", `/api/v1/approvals/${sheet.card.request_id}/approve`, {
        state_version: detail?.state_version ?? sheet.card.state_version,
        ...(stepToken ? { step_up_token: stepToken } : {}),
      });
      if (result.error?.code === "STEP_UP_REQUIRED") {
        setNeedStepUp(true);
        setMessage(result.error.message || "Step-up is required");
        return;
      }
      if (result.status < 200 || result.status >= 300) {
        setMessage(result.error?.message || "Approve was refused");
        return;
      }
      const state = (result.data as { state?: string })?.state || "updated";
      setMessage(`Recorded ${state}`);
    } finally {
      setBusy(false);
    }
  }

  async function reject() {
    setBusy(true);
    setMessage("");
    try {
      const result = await apiSend(token, "POST", `/api/v1/approvals/${sheet.card.request_id}/reject`, {
        reason: reason.trim(),
        state_version: detail?.state_version ?? sheet.card.state_version,
      });
      if (result.status < 200 || result.status >= 300) {
        setMessage(result.error?.message || "Reject was refused");
        return;
      }
      setMessage("Recorded rejected");
    } finally {
      setBusy(false);
    }
  }

  const title = sheet.kind === "approve" ? "Approve request" : sheet.kind === "reject" ? "Reject request" : "Flag for review";
  return (
    <div className="scrim" role="presentation">
      <section className="sheet" role="dialog" aria-labelledby="sheet-title" data-testid="approval-sheet">
        <h2 id="sheet-title">{title}</h2>
        <p>Live state {detail?.state || "loading"}</p>
        <p>Amount {detail?.amount?.amount || sheet.card.amount?.amount || "—"} {detail?.amount?.currency || ""}</p>
        <p>Hash {(detail?.snapshot_hash || "").slice(0, 12) || "—"}</p>
        {(detail?.fraud_hints || []).map((hint) => <p key={hint} className="alert">{hint}</p>)}
        {sheet.kind === "reject" ? (
          <label>
            Reason
            <textarea value={reason} onChange={(event) => setReason(event.target.value)} required />
          </label>
        ) : null}
        {sheet.kind === "approve" && needStepUp ? (
          <label>
            Step-up code
            <input inputMode="numeric" autoComplete="one-time-code" value={stepCode} onChange={(event) => setStepCode(event.target.value)} />
          </label>
        ) : null}
        {message ? <p role="alert">{message}</p> : null}
        <div className="row">
          {sheet.kind === "approve" ? <button type="button" disabled={busy} onClick={() => void approve()}>Approve</button> : null}
          {sheet.kind === "reject" ? <button type="button" disabled={busy || reason.trim() === ""} onClick={() => void reject()}>Reject</button> : null}
          {sheet.kind === "flag" ? (
            <button type="button" onClick={() => { onFlagged(sheet.card.request_id); setMessage("Flagged for review"); }}>
              Flag for review
            </button>
          ) : null}
          <button type="button" onClick={onClose}>Close</button>
        </div>
      </section>
    </div>
  );
}
