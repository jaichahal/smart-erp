import { PointerEvent, useEffect, useState } from "react";
import { apiCall, apiFailure } from "./api";
import type { Profile } from "./session";

type PurchaseRow = { id?: string; number?: string; status?: string; approval_id?: string };

function rowsFrom(body: unknown): PurchaseRow[] | null {
  const data = (body as { data?: unknown } | null)?.data;
  if (Array.isArray(data)) {
    return data as PurchaseRow[];
  }
  if (data && typeof data === "object") {
    const record = data as { items?: PurchaseRow[]; lpos?: PurchaseRow[] };
    if (Array.isArray(record.items)) {
      return record.items;
    }
    if (Array.isArray(record.lpos)) {
      return record.lpos;
    }
  }
  return null;
}

export function PurchaseList({ profile }: { profile: Profile }) {
  const [rows, setRows] = useState<PurchaseRow[] | null>(null);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const [sheet, setSheet] = useState<PurchaseRow | null>(null);
  const [reason, setReason] = useState("");
  const [start, setStart] = useState<number | null>(null);
  const [actionError, setActionError] = useState("");

  useEffect(() => {
    const path = "/api/v1/lpos";
    let cancel = false;
    void apiCall(profile.accessToken, "GET", path).then((result) => {
      if (cancel) {
        return;
      }
      if (!result.ok) {
        setError(apiFailure("GET", path, result));
        return;
      }
      const found = rowsFrom(result.body);
      if (!found) {
        setError(`GET ${path} failed (${result.status}) unexpected purchase list payload`);
        return;
      }
      setRows(found);
      setStatus("loaded");
    });
    return () => {
      cancel = true;
    };
  }, [profile.accessToken]);

  async function reject() {
    if (!sheet || reason.trim() === "") {
      return;
    }
    const path = sheet.approval_id
      ? `/api/v1/approvals/${sheet.approval_id}/reject`
      : `/api/v1/lpos/${sheet.id}/reject`;
    const result = await apiCall(profile.accessToken, "POST", path, { reason: reason.trim(), state_version: 0 });
    if (!result.ok) {
      setActionError(apiFailure("POST", path, result));
      return;
    }
    setSheet(null);
  }

  function finishDrag(event: PointerEvent, row: PurchaseRow) {
    if (start === null) {
      return;
    }
    const dx = event.clientX - start;
    setStart(null);
    if (dx > -48) {
      return;
    }
    setReason("");
    setActionError("");
    setSheet(row);
  }

  return (
    <section>
      <h1>Purchase list</h1>
      {error ? <p data-testid="purchase-list-error">{error}</p> : null}
      {status ? <p data-testid="purchase-list-status">{status}</p> : null}
      {rows ? (
        <div data-testid="purchase-list">
          {rows.length ? (
            rows.map((row) => (
              <article
                className="row"
                key={row.id || row.number}
                data-testid="purchase-row"
                onPointerDown={(event) => setStart(event.clientX)}
                onPointerUp={(event) => finishDrag(event, row)}
              >
                <h2>{row.number || row.id}</h2>
                <p>{row.status}</p>
              </article>
            ))
          ) : (
            <p>No purchase documents</p>
          )}
        </div>
      ) : null}
      {sheet ? (
        <div data-testid="approval-sheet" role="dialog" aria-label="Review">
          <h2>Review</h2>
          <label>
            Reject reason
            <input value={reason} onChange={(event) => setReason(event.target.value)} />
          </label>
          <button type="button" disabled={reason.trim() === ""} onClick={() => void reject()}>
            Reject
          </button>
          <button type="button" onClick={() => setSheet(null)}>
            Close
          </button>
          {actionError ? <p role="alert">{actionError}</p> : null}
        </div>
      ) : null}
    </section>
  );
}
