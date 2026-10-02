# Client API needs

Recorded against the live API on 2026-09-25. Times are from the Mac probe unless noted.

## GET /api/v1/lpos

The list handler has no limit or cursor. It loads every document id. A bearer GET returned 200 in 5ms and 6ms with `data: []`.

The phone test `purchaseListCallsLiveApi` then passed in 2.925s on RFCY720W1NN. The earlier 20s `ComposeTimeoutException` was the UI test waiting for tags that never appeared. The client read timeout is 8s, so that wait was not a hung socket and was not a missing limit query. The handler does not read a limit.

The other side is pagination. The current database is fast, and the list still loads every id before each document. Do not add a query the handler ignores.

## GET /api/v1/vendors/dashboard

`sku` is accepted only when `uuid.Parse` succeeds. `sku=RM-TEST` is 400 in 4ms to 5ms: `The purchase document is not valid`, details field `sku`. There is no route that turns a SKU code into that id.

A UUID that is not in `erp.purchase_skus` is 500 in 7ms: `internal error`. After a real raw-material row existed, the same call was 200 in 9ms. A missing SKU should be a not-found or validation response, not an internal error.

No bearer is 401 `Authentication failed` in about 1ms. With the console bearer and a bad SKU, the same route is the 400 above. The Android call already uses the session exchange, which sets `Authorization: Bearer` the same way the inbox GET does.

## POST /api/v1/sales-orders and POST /api/v1/receipts

The running sales create is the masters handler. It requires `If-Match` before the body. Create uses `0`. The line is `sku_id`, `qty`, `uom`, and `unit_price` as strings. A directory role stored as `Sales Agent` does not match `role_permissions.role_name` `sales_agent`, so the customer row stays hidden and the handler returns `customer not found`.

A cash receipt rejects unknown fields such as `on_account` with `invalid JSON body`. The accepted body needs `customer_id`, `method`, `amount`, `collector_id`, `posted_on`, `cash_account_id`, and `allocations`. There is no list route for cash accounts. The insert also needs an `erp.ar_customers` row. A missing account is 400 `An account is required.`
