import { FormEvent, useState } from "react";
import { apiCall, apiFailure } from "./api";
import type { Profile } from "./session";

function documentNumber(body: unknown): string {
  const data = (body as { data?: Record<string, unknown> } | null)?.data;
  if (!data) {
    return "";
  }
  const number = data.number || data.doc_number || data.id;
  return typeof number === "string" ? number : "";
}

export function SalesOrder({ profile }: { profile: Profile }) {
  const [customerId, setCustomerId] = useState("");
  const [sku, setSku] = useState("");
  const [quantity, setQuantity] = useState("1");
  const [unitPrice, setUnitPrice] = useState("");
  const [number, setNumber] = useState("");
  const [error, setError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setNumber("");
    const path = "/api/v1/sales-orders";
    const result = await apiCall(profile.accessToken, "POST", path, {
      customer_id: customerId,
      lines: [
        {
          sku,
          quantity,
          uom: "ea",
          unit_price: { amount: unitPrice, currency: "AED" },
        },
      ],
    });
    if (!result.ok) {
      setError(apiFailure("POST", path, result));
      return;
    }
    const found = documentNumber(result.body);
    if (!found) {
      setError(`POST ${path} failed (${result.status}) unexpected sales order payload`);
      return;
    }
    setNumber(found);
  }

  return (
    <section>
      <h1>Sales order</h1>
      <form onSubmit={submit}>
        <label>
          Customer
          <input value={customerId} onChange={(event) => setCustomerId(event.target.value)} />
        </label>
        <label>
          SKU
          <input value={sku} onChange={(event) => setSku(event.target.value)} />
        </label>
        <label>
          Quantity
          <input value={quantity} onChange={(event) => setQuantity(event.target.value)} inputMode="decimal" />
        </label>
        <label>
          Unit price
          <input value={unitPrice} onChange={(event) => setUnitPrice(event.target.value)} inputMode="decimal" />
        </label>
        <button type="submit">Submit order</button>
      </form>
      {error ? <p data-testid="sales-order-error">{error}</p> : null}
      {number ? <p data-testid="sales-order-number">{number}</p> : null}
    </section>
  );
}

export function CollectionReceipt({ profile }: { profile: Profile }) {
  const [method, setMethod] = useState("cash");
  const [amount, setAmount] = useState("");
  const [chequeNumber, setChequeNumber] = useState("");
  const [bank, setBank] = useState("");
  const [chequeDate, setChequeDate] = useState("");
  const [number, setNumber] = useState("");
  const [error, setError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setNumber("");
    const path = "/api/v1/receipts";
    const result = await apiCall(profile.accessToken, "POST", path, {
      method,
      amount: { amount, currency: "AED" },
      allocations: [],
      on_account: { amount, currency: "AED" },
      cheque: method === "cheque" ? { number: chequeNumber, bank, date: chequeDate } : undefined,
    });
    if (!result.ok) {
      setError(apiFailure("POST", path, result));
      return;
    }
    const found = documentNumber(result.body);
    if (!found) {
      setError(`POST ${path} failed (${result.status}) unexpected receipt payload`);
      return;
    }
    setNumber(found);
  }

  return (
    <section>
      <h1>Collection receipt</h1>
      <form onSubmit={submit}>
        <label>
          Method
          <select value={method} onChange={(event) => setMethod(event.target.value)}>
            <option value="cash">cash</option>
            <option value="cheque">cheque</option>
            <option value="transfer">transfer</option>
          </select>
        </label>
        <label>
          Amount
          <input value={amount} onChange={(event) => setAmount(event.target.value)} inputMode="decimal" />
        </label>
        {method === "cheque" ? (
          <>
            <label>
              Cheque number
              <input value={chequeNumber} onChange={(event) => setChequeNumber(event.target.value)} />
            </label>
            <label>
              Bank
              <input value={bank} onChange={(event) => setBank(event.target.value)} />
            </label>
            <label>
              Cheque date
              <input value={chequeDate} onChange={(event) => setChequeDate(event.target.value)} />
            </label>
          </>
        ) : null}
        <button type="submit">Record receipt</button>
      </form>
      {error ? <p data-testid="collection-receipt-error">{error}</p> : null}
      {number ? <p data-testid="collection-receipt-number">{number}</p> : null}
    </section>
  );
}
