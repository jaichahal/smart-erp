import { FormEvent, PointerEvent, useState } from "react";
import { apiCall, apiFailure } from "./api";
import { canGateVendor } from "./persona";
import type { Profile } from "./session";

type Money = { amount?: string; currency?: string };
type Source = {
  id?: string;
  number?: string;
  unit_price?: Money;
  currency?: string;
  effective_from?: string;
  effective_to?: string;
};
type VendorSku = { sku?: string; active_invoice_count?: number; past_invoice_count?: number };
type VendorRow = {
  id?: string;
  name?: string;
  status?: string;
  active_invoice_count?: number;
  past_invoice_count?: number;
  skus?: VendorSku[];
  approval_id?: string;
};
type BlockedRow = { id?: string; name?: string; reason?: string };
type Dashboard = {
  vendors?: VendorRow[];
  best_price?: { source?: Source } | null;
  blocked?: BlockedRow[];
};

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" ? (value as Record<string, unknown>) : null;
}

export function VendorDashboard({ profile }: { profile: Profile }) {
  const [sku, setSku] = useState("");
  const [dashboard, setDashboard] = useState<Dashboard | null>(null);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const [sheet, setSheet] = useState<VendorRow | null>(null);
  const [reason, setReason] = useState("");
  const [actionError, setActionError] = useState("");
  const gate = canGateVendor(profile);

  async function applyFilter(event: FormEvent) {
    event.preventDefault();
    setError("");
    setStatus("");
    setDashboard(null);
    const path = `/api/v1/vendors/dashboard?sku=${encodeURIComponent(sku)}`;
    const result = await apiCall(profile.accessToken, "GET", path);
    if (!result.ok) {
      setError(apiFailure("GET", path, result));
      return;
    }
    const data = asRecord(asRecord(result.body)?.data);
    if (!data || !Array.isArray(data.vendors)) {
      setError(`GET ${path} failed (${result.status}) unexpected vendor dashboard payload`);
      return;
    }
    setDashboard(data as Dashboard);
    setStatus("loaded");
  }

  function openSheet(row: VendorRow, dx: number) {
    if (dx > -48) {
      return;
    }
    setReason("");
    setActionError("");
    setSheet(row);
  }

  async function commit(kind: "approve" | "blacklist" | "reject") {
    if (!sheet?.id) {
      return;
    }
    if (kind === "reject" && reason.trim() === "") {
      return;
    }
    const path =
      kind === "reject" && sheet.approval_id
        ? `/api/v1/approvals/${sheet.approval_id}/reject`
        : `/api/v1/vendors/${sheet.id}/${kind}`;
    const result = await apiCall(profile.accessToken, "POST", path, {
      reason: reason.trim(),
      state_version: 0,
    });
    if (!result.ok) {
      setActionError(apiFailure("POST", path, result));
      return;
    }
    setSheet(null);
  }

  const source = dashboard?.best_price?.source;

  return (
    <section>
      <h1>Vendor dashboard</h1>
      <form onSubmit={applyFilter}>
        <label>
          Raw-material SKU
          <input value={sku} onChange={(event) => setSku(event.target.value)} />
        </label>
        <button type="submit">Apply SKU filter</button>
      </form>
      {error ? <p data-testid="vendor-dashboard-error">{error}</p> : null}
      {status ? <p data-testid="vendor-dashboard-status">{status}</p> : null}
      {dashboard ? (
        <>
          <div data-testid="invoice-counts">
            {dashboard.vendors?.length ? (
              dashboard.vendors.map((row) => (
                <VendorLine key={row.id || row.name} row={row} onSwipe={openSheet} />
              ))
            ) : (
              <p>No vendors approved for this SKU</p>
            )}
          </div>
          <div data-testid="best-price">
            {source ? (
              <>
                <p data-testid="best-price-source-id">{source.id}</p>
                <p data-testid="best-price-source-number">{source.number}</p>
                <p data-testid="best-price-unit-price">{source.unit_price?.amount}</p>
                <p data-testid="best-price-currency">{source.unit_price?.currency || source.currency}</p>
                <p data-testid="best-price-window">
                  {source.effective_from} to {source.effective_to}
                </p>
              </>
            ) : (
              <p data-testid="best-price-empty">No best price</p>
            )}
          </div>
          <div data-testid="blocked-group">
            <h2>Blocked</h2>
            {dashboard.blocked?.length ? (
              dashboard.blocked.map((row) => (
                <p key={row.id || row.name}>
                  {row.name}: {row.reason}
                </p>
              ))
            ) : (
              <p>No blocked vendors</p>
            )}
          </div>
        </>
      ) : null}
      {sheet ? (
        <ReviewSheet
          gate={gate}
          reason={reason}
          actionError={actionError}
          onReason={setReason}
          onApprove={() => void commit("approve")}
          onBlacklist={() => void commit("blacklist")}
          onReject={() => void commit("reject")}
          onClose={() => setSheet(null)}
        />
      ) : null}
    </section>
  );
}

function VendorLine({ row, onSwipe }: { row: VendorRow; onSwipe: (row: VendorRow, dx: number) => void }) {
  const [start, setStart] = useState<number | null>(null);
  function down(event: PointerEvent) {
    setStart(event.clientX);
  }
  function up(event: PointerEvent) {
    if (start === null) {
      return;
    }
    onSwipe(row, event.clientX - start);
    setStart(null);
  }
  return (
    <article className="row" data-testid="vendor-row" onPointerDown={down} onPointerUp={up}>
      <h2>{row.name}</h2>
      <p>{row.status}</p>
      <p data-testid="active-invoice-count">Active invoices {row.active_invoice_count ?? 0}</p>
      <p data-testid="past-invoice-count">Past invoices {row.past_invoice_count ?? 0}</p>
      {row.skus?.map((sku) => (
        <p key={sku.sku}>
          {sku.sku} active {sku.active_invoice_count ?? 0} past {sku.past_invoice_count ?? 0}
        </p>
      ))}
    </article>
  );
}

function ReviewSheet(props: {
  gate: boolean;
  reason: string;
  actionError: string;
  onReason: (value: string) => void;
  onApprove: () => void;
  onBlacklist: () => void;
  onReject: () => void;
  onClose: () => void;
}) {
  return (
    <div data-testid="approval-sheet" role="dialog" aria-label="Review">
      <h2>Review</h2>
      {props.gate ? (
        <>
          <button type="button" onClick={props.onApprove}>
            Approve
          </button>
          <button type="button" onClick={props.onBlacklist}>
            Blacklist
          </button>
        </>
      ) : null}
      <label>
        Reject reason
        <input value={props.reason} onChange={(event) => props.onReason(event.target.value)} />
      </label>
      <button type="button" disabled={props.reason.trim() === ""} onClick={props.onReject}>
        Reject
      </button>
      <button type="button" onClick={props.onClose}>
        Close
      </button>
      {props.actionError ? <p role="alert">{props.actionError}</p> : null}
    </div>
  );
}
