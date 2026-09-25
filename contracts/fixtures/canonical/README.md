# Canonical JSON and hash test vectors

Shared fixtures for the audit hash chain. The Go kit (`apps/api/internal/kit/canon`) is the
reference implementation and **generates** the fixture files in this directory; the console,
Android and iOS clients consume them so that every surface computes the same hash for the same
row (B4). Do not hand-write or hand-edit fixture files; run `just gen`.

Source of truth: `docs/spec/05-security-and-audit.md` "Hash chain". This README restates the rules
so a client implementer does not need to read the whole spec.

## Canonical form

Given a row and its child rows, the canonical form is a single JSON document produced as follows.

1. **Sorted keys.** Every object's keys are sorted by Unicode code point (byte order of the UTF-8
   key), at every nesting level.
2. **No whitespace.** No spaces, tabs or newlines anywhere outside string values. Separators are
   exactly `,` and `:`.
3. **Decimals as strings.** Every money or quantity value is a decimal string exactly as stored
   (`"1234.56"`, never `1234.56`), with the scale the column defines and no exponent. Money is the
   `{ "amount": "...", "currency": "AED" }` object.
4. **Timestamps as UTC ISO 8601.** `YYYY-MM-DDTHH:MM:SS(.ffffff)?Z`. Always `Z`, never an offset.
   Precision is whatever the database stored; do not truncate or pad.
5. **Child rows included, sorted by `line_no`.** Each child table appears as an array under its
   own key, ordered ascending by `line_no`, each child serialised with the same rules.
6. **Referenced ledger line ids included.** The ids of ledger lines the document produced appear
   as an array of strings in ascending order under `ledger_line_ids`.
7. **`prev_hash` and `hash` excluded.** They are never part of the canonical form of the row
   whose hash they describe. `chain_seq` **is** included.
Rules 1 to 7 are the spec. Rules 8 to 10 are the encoding details the kit fixes so that every
language produces the same bytes; where this README and the generated vectors disagree, the
vectors win and this README is corrected in the same PR.

8. **Strings** use JSON escaping with the minimal set: `"`, `\`, control characters as `\uXXXX`
   (lower-case hex), and nothing else escaped. Non-ASCII is emitted as UTF-8, not `\u` escaped.
9. **Null** fields are emitted as `null`; absent and null are the same thing and the generator
   always emits every column of the table so the key set is stable.
10. **Booleans** are `true` / `false`. **Integers** (ids, counts, `chain_seq`, `state_version`)
    are JSON numbers without a fraction.

## Hash

```
hash = hex( sha256( canonical_bytes || prev_hash_bytes ) )
```

- `canonical_bytes` is the UTF-8 encoding of the canonical form above.
- `prev_hash_bytes` is the UTF-8 encoding of the **hex string** of the previous row's `hash`
  (64 lower-case hex characters), appended directly with no separator. For the first row of a
  company's chain `prev_hash` is the 64-character string of zeros.
- The result is the lower-case hex encoding of the 32-byte digest.

Per company there is one chain covering all immutable rows in append order; the appending
transaction takes `pg_advisory_xact_lock(company_chain_key)` and reads the head from `chain_head`
(B5). Timestamps in the row come from `clock_timestamp()` in the database (B8).

## Fixture file layout (generated)

```
contracts/fixtures/canonical/
  README.md                 this file (hand-written)
  vectors.json              generated: array of { name, input, prev_hash, canonical, hash }
  <name>.input.json         generated: pretty-printed input row, for readability
```

Each vector in `vectors.json`:

| field | meaning |
| --- | --- |
| `name` | short identifier, e.g. `sales_invoice_two_lines`, `unicode_party_name`, `first_in_chain` |
| `input` | the row with child arrays in arbitrary key and line order, as the generator received it |
| `prev_hash` | 64 hex chars |
| `canonical` | the exact canonical string a conforming implementation must produce |
| `hash` | the expected `hash` |

A client test loads `vectors.json`, canonicalises `input`, asserts equality with `canonical`
byte for byte, then hashes and asserts equality with `hash`. The Go kit has the same test.

Vectors the generator must include, at minimum: first row in a chain (zero `prev_hash`); a
document with child lines given out of `line_no` order; money with trailing zeros (`"100.00"`);
negative amounts; a null party; Arabic and emoji in a string; a timestamp with microseconds and one
without; nested `context` objects with keys that sort differently by code point and by locale.
