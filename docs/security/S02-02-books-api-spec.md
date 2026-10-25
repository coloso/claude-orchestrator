# S02-02: Security review of the books API spec (S-02)

Reviewed before the HTTP adapter exists. Date: 2026-10-25. Reviewer role: security.

## Scope reviewed

- `backend/api/openapi.yaml` at commit `4a7b12f`: the operations `createBook`, `listBooks`, `getBook`, `updateBook`, `deleteBook`, and the schemas `Book`, `BookInput`, `BookUpdate`, `BookList`, `Error`.
- The ogen-generated code in `backend/internal/oas` (v1.24.0 of ogen). It is what actually decodes and validates requests, so spec keywords are only as good as what it does with them.
- The code around it that changes the effect of the spec: `internal/server/server.go`, `internal/handler/handler.go`, the `http.Server` setup in `cmd/server/main.go`, and the `catalog/0001` `$jsonSchema` validator.
- Not reviewed: the domain, use cases, repositories and HTTP adapter (not written yet), `docker-compose.yml` and the Dockerfile. A review of the implementation is proposed as a follow-up.

### How the findings were verified

The book operations still return 501, but the generated decoders and validators already run before the handler. I probed `server.New(&handler.Handler{})` with about 70 crafted requests in a temporary test, then deleted the test. A **400/413/415** means the request was rejected before any handler code. A **501** means it was accepted and would have reached the adapter. The probe only touched request decoding: the file was removed and nothing outside `docs/security/` is changed.

## Accepted risks

| Risk | Why accepted |
|---|---|
| **No authentication or authorization** (stated once here, not repeated per finding) | The demo has no users. Anyone who can reach the port can create, change and hard-delete any book. |
| Enumeration of book IDs and ISBNs | IDs are sequential and `listBooks` is open, so 404 vs 200 and 409 on a duplicate ISBN reveal nothing that the list does not already show. Without auth there is nothing to protect. |
| Plain HTTP, no TLS | Local demo. Needs a TLS-terminating proxy before any real deployment. |
| Hard delete, no audit trail | Product decision (S-02); it is only a problem together with the missing auth. |

## Threat model

**Assets**: availability of the API and of MongoDB (CPU, memory, disk); integrity of the catalog (ISBN uniqueness, version counter, the invariants the domain and the `$jsonSchema` enforce); the process itself (no crashes, no internal details in responses).

**Entry points**: five `/api/books*` routes. Untrusted data: JSON bodies of `POST` and `PUT`, query parameters `author`, `limit`, `offset`, path parameter `id`, headers (`Content-Type`, `Content-Length`).

**Trust boundaries**: network → ogen decoder/validator (generated, runs before the handler) → HTTP adapter (maps oas ↔ domain) → domain/app (the real validation) → repository → MongoDB (second line of defense: `$jsonSchema`).

| STRIDE | Threat | Where it applies |
|---|---|---|
| **S**poofing | Out of scope (no auth). | |
| **T**ampering | Mass assignment of `id`, `version`, `createdAt`, `updatedAt`. NoSQL operator injection through `author`. Lost update through a stale `version`. | SEC-S02-4; "checked and fine" below |
| **R**epudiation | No audit log of who changed what. | Accepted with the auth risk |
| **I**nformation disclosure | Library error text and Go type names in 400 responses. Missing `nosniff` / `Cache-Control`. | SEC-S02-6 |
| **D**enial of service | Huge or slow bodies, huge arrays, deep JSON nesting, deep `skip`, unlimited writes, no rate limit. | SEC-S02-1, 2, 3, 5 |
| **E**levation of privilege | Not applicable (no privileges). | |

## Findings

| ID | Title | Severity | Location | Impact | Recommendation | Follow-up |
|---|---|---|---|---|---|---|
| SEC-S02-1 | Request body size is unbounded | medium | `internal/oas/oas_request_decoders_gen.go:47` (`io.ReadAll(r.Body)`); no limit in `internal/server/server.go` | A single request with a multi-MB body is read fully into memory and decoded. | `http.MaxBytesReader` with 64 KiB, answer 413. | Proposal 2 |
| SEC-S02-2 | Server timeouts incomplete; header size at the 1 MiB default | medium | `cmd/server/main.go:48` (only `ReadHeaderTimeout`) | Slow-body clients hold a goroutine and a buffer open without limit. | Add `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `MaxHeaderBytes`. | Proposal 3 |
| SEC-S02-3 | No rate limiting, and no cap on catalog growth | medium | whole API; `createBook` | An anonymous client can fill the database and slow `listBooks`. | Per-client token bucket, 429. | Proposal 4 |
| SEC-S02-4 | Unknown body properties are silently ignored (latent mass assignment) | low | `openapi.yaml:191` (`BookInput`), `:217` (`BookUpdate`) | Not exploitable today (see below). A future hand-written mapper or a schema change could let `id`/`version` through unnoticed. | `additionalProperties: false` on both input schemas. | Proposal 1 |
| SEC-S02-5 | `offset` has no upper bound | low | `openapi.yaml:73-80` | `offset=9223372036854775807` is accepted; MongoDB walks the index per request. | Add `maximum: 100000`. | Proposal 1 |
| SEC-S02-6 | Transport errors expose internals; no security headers | low | `internal/server/server.go:29-31` | 400 responses carry Go type names, `strconv` text and operation names. Nested JSON is amplified about 7× in the response. No `nosniff`, `Cache-Control`, CSP. | Fixed, generic messages; add the headers. | Proposal 2 |
| SEC-S02-7 | Domain must handle Unicode and control characters explicitly | low | future `catalog/domain`; ogen passes these through (probe) | Invalid UTF-8, `\u0000`, non-ASCII digits and a trailing newline get past the decoder. The DB validator is not a reliable backstop for all of them. | List of rules below. | Proposal 5 |
| SEC-S02-8 | Spec hygiene: `version` has no minimum, `author` no `maxLength`, error statuses incomplete | low | `openapi.yaml:61-63`, `:243-246`, operation descriptions | Version `0` or `-5` gives 409 instead of 400. A 1 MB `author` filter is accepted. Real statuses (400 for a bad id, 413, 415) are undocumented. | See below. | Proposal 1 |

Details follow.

### SEC-S02-1: Request body size is unbounded (medium)

ogen reads the whole body with `io.ReadAll`, copies it (`rawBody = append(rawBody, buf...)`), and only then decodes and validates. The server sets no limit, so `Content-Length` of any size is accepted.

Probe results (in-process, so no network time):
- A 50 MiB body with one huge unknown property was accepted (501, 34 ms).
- A body with **2,000,000 entries in `authors`** was decoded into a `[]string` and accepted (501, 63 ms).
- The two requests (57 MiB of body) allocated about **516 MiB** in total, roughly 9× the body size.

**Do `maxItems` / `maxLength` help? No, not against this.** ogen decodes the full array and all strings before it calls `Validate()`, so by the time `maxItems: 20` would reject the 2M-entry array, the memory is already spent. The only effective control is a limit on bytes read, before decoding. The design decision to leave string and array limits to the domain is therefore sound for security as well:
- keywords would not reduce consumption;
- they would be checked on untrimmed input, so a title of 300 characters plus spaces would be rejected by the schema but accepted by the domain, which trims;
- `isbn` has no sensible finite `maxLength`, because hyphens and spaces are removed first.

**Recommended limit: 64 KiB.** Worst legal body: 20 authors × 200 + title 300 = 4,300 code points. Even if every one is written as a 12-byte JSON surrogate-pair escape that is about 52 KB, plus the other fields. 64 KiB never rejects a valid book. The middleware must also map `*http.MaxBytesError` to 413 in `server.writeError`, because ogen's default mapping would give 400 (or 500).

### SEC-S02-2: Server timeouts incomplete (medium)

`http.Server` has only `ReadHeaderTimeout: 5s`. With `POST` and `PUT` there is now a body to read, and without `ReadTimeout` a client can send one byte every few seconds and keep the connection and its buffer for as long as it wants (slow body). `WriteTimeout` and `IdleTimeout` are also unset, and `MaxHeaderBytes` is Go's 1 MiB default, which also bounds the request line and so the length of the query string. The handler layer accepted a 60,000-character `author` in the probe; nothing in the spec or the code caps it below that header limit. Suggested: `ReadTimeout 10s`, `WriteTimeout 15s`, `IdleTimeout 60s`, `MaxHeaderBytes 16 KiB`.

### SEC-S02-3: No rate limiting, no cap on catalog size (medium)

Recorded as required: nothing limits how fast a client can call. For this API the effect is concrete: `createBook` stores up to roughly 5 KB per request, the ISBN space is about 2×10⁹ valid values, and nothing stops a client from filling the collection. This also feeds SEC-S02-5, because `skip` and `countDocuments` cost grows with the collection size. A per-client token bucket (key: `RemoteAddr` host; `X-Forwarded-For` must not be trusted without a known proxy) with lower limits for writes limits the damage. A maximum number of books is a product decision and is not proposed.

### SEC-S02-4: Mass assignment (low)

**Can a client set `id`, `version`, `createdAt`, `updatedAt` through `BookInput` or `BookUpdate`?** Not today, but only by accident of structure:
- Probe: `POST` with `"id":5,"version":9,"createdAt":"x","updatedAt":1,"foo":{...}` was accepted (501). ogen's generated decoder has `default: return d.Skip()`, so unknown properties are dropped silently. The generated `oas.BookInput` has no field to put them in, so they cannot reach the adapter.
- `BookUpdate.version` is the intended optimistic-lock token; the other three fields do not exist in the input schemas.
- Duplicate keys are accepted and the last one wins (a single parser, so no parser differential).

**Should the schemas say `additionalProperties: false`?** Yes, on `BookInput` and `BookUpdate` only. ogen supports it (`DenyAdditionalProps`): the generated decoder then fails with `unexpected field "id"` and the request gets a 400 naming the field. That makes a client mistake visible and turns the current accident into a guarantee in the spec and in a test. Do **not** put it on `Book`, `BookList` or `Error`: the generated client would then reject any field the server adds later.

Trade-off to accept: a client that does read-modify-write by sending a whole `Book` back to `PUT` (with `id`, `createdAt`, `updatedAt`) will get 400. The message names the field, and the spec description should say that only the editable fields and `version` are sent.

### SEC-S02-5: Deep offsets (low)

`limit` (1–100) and the path `id` are bounded; `offset` has only a minimum. The probe accepts `offset=9223372036854775807` and rejects `…808` (out of range). MongoDB cannot jump to an offset: `skip(n)` walks the `_id` index entries, so the cost is proportional to min(`offset`, collection size). With a small catalog this is harmless, which is why it is low, but combined with SEC-S02-3 an attacker controls the collection size. Recommend `maximum: 100000` (1,000 pages of 100) and a description that narrower queries use `author`. Cursor-based paging is out of scope for S-02 and would be the proper fix. For the adapter, see the note on `total` below.

### SEC-S02-6: Transport errors and headers (low)

`writeError` returns `err.Error()` from ogen verbatim. Observed responses:
- `operation CreateBook: decode request: decode application/json: decode BookInput: callback: decode field "publishedYear": unexpected floating point character`
- `operation ListBooks: decode params: query: "offset": strconv.Atoi: parsing "9223372036854775808": value out of range`
- For deeply nested unknown properties (200,000 `[` in a 10 MB body), the message repeats `array: ` for every level the parser reached: **70 KB of response for a 10 MB request**.

None of it is secret, but it ties clients to Go type names and library wording, and it contradicts the convention of a short `{"error": "<message>"}`. Domain validation messages ("title must be 1-300 characters") come from the adapter and are not affected. For transport errors, keep the parameter or field name and use a fixed text.

No response has `X-Content-Type-Options`, `Cache-Control` or `Content-Security-Policy`; only `Content-Type` is set. For an API that now returns data, set `nosniff`, `Cache-Control: no-store` and `Content-Security-Policy: default-src 'none'` on every response, errors included.

### SEC-S02-7: Unicode and character edge cases (low)

Observed: all of these pass the ogen decoder and reach the domain unchanged: raw invalid UTF-8 bytes, a lone surrogate escape (`"\ud800"`), `"\u0000"` in a title, and any string length. The domain must therefore:
1. Reject invalid UTF-8 (`utf8.ValidString`) and control characters (`unicode.IsControl`, including NUL) in `title`, `authors` and `isbn`. Otherwise a value that MongoDB rejects turns into a 500 instead of a 400.
2. Count and trim as designed (code points, after trimming), but check the ISBN with ASCII `'0'..'9'`, not `unicode.IsDigit`: Arabic-Indic and full-width digits are not ISBN digits and must give 400.
3. Not rely on the `$jsonSchema` `pattern` for `language` and `isbn`. MongoDB uses PCRE, where `$` also matches before a final newline, so `"en\n"` passes the database validator. Go's `regexp` `$` does not, so the domain check (`^[a-z]{2}$`) is correct; keep it in the domain. Because the domain strips the ISBN to digits, only `language` is affected.

### SEC-S02-8: Spec hygiene (low)

- `BookUpdate.version` has no `minimum`. Stored versions start at 1 (story and `catalog/0001` validator both say `minimum: 1`). Add `minimum: 1`; `0` and negative values then give 400 instead of a misleading 409. `Book.version` and `BookList.total` need nothing; they are responses.
- The `author` query parameter has no `maxLength`. An author is at most 200 characters after trimming and the filter is an exact match, so any longer value can never match. `maxLength: 200` is exact, not a new rule, and avoids sending a ~1 MB string to MongoDB. (Query parameters are not trimmed by the domain, unlike body fields.)
- Error descriptions follow the CLAUDE.md conventions (only the success response plus `default: $ref Error`; error statuses listed in the description; no per-operation 4xx). Gaps in accuracy only: `getBook`, `updateBook`, `deleteBook` can also return **400** for a malformed id (`0`, `abc`, an overflowing number; the probe confirms it); `createBook`/`updateBook` return **415** for a non-JSON `Content-Type` today and **413** once SEC-S02-1 is fixed.

## Checked and fine

| Check | Result |
|---|---|
| **Integer handling, JSON numbers** | `publishedYear`/`pages` are `type: integer` without `format`, which ogen generates as Go `int`. Probe: `1.5`, `1e3`, `1.0`, `"2000"`, `null`, `true` → 400; `99999999999999999999` → 400 "value out of range"; `-1`, `0`, `9223372036854775807` are accepted by ogen and left to the domain's 1450–2100 and 1–10000 ranges. No silent truncation or wrap-around. |
| `version` (int64) | `1.0`, `1e2`, `null`, `9223372036854775808` → 400; missing → 400 (required); `0`/negative accepted by ogen (SEC-S02-8). |
| Path `id` (int64, ≥1) | `0`, `-1`, `abc`, `1.0`, `1e2`, `%00`, `1%2F2`, `9223372036854775808` → 400; `9223372036854775807` → passes to the handler (404 expected). `01` and `+1` are accepted and equal `1` (non-canonical aliases; harmless without caching). |
| int vs int64 on the wire | Consistent with CLAUDE.md: `int64` for `id`, `version`, `total`; `int` for `publishedYear`, `pages`, `limit`, `offset`. The `catalog/0001` validator requires BSON `int` (int32) for `publishedYear`/`pages`; the domain ranges guarantee they fit. |
| Query parameters | `limit` `0`, `101`, `abc`, empty, repeated (`limit=1&limit=2`) → 400. `offset` `-1`, `1e2`, `+5`, empty, `…808` → 400. Unknown parameters (`foo=bar`, `$where=1`) are ignored; they can never reach the query. `author[$ne]=x` is just an unused parameter, and `author` is a plain string: **user input cannot become a `bson.M` key or `$` operator** as long as the adapter builds `bson.D{{"authors", author}}` with the value as a string (to be verified in the implementation review). |
| Response size | `limit` ≤ 100 bounds a list page (≈ 100 × 6 KB). `sort` is fixed (id ascending), so there is no user-controlled sort key. |
| Internal fields | `Book` exposes `id`, `isbn`, `title`, `authors`, `publishedYear`, `language`, `pages`, `createdAt`, `updatedAt`, `version`. Nothing from `counters` or Mongo (`_id` is mapped to `id`). |
| Malformed JSON | Arrays, `null` for `authors`, numbers inside `authors`, trailing data (`{...}{}`), empty body, missing `Content-Type` → 400. `text/plain` → 415. |
| Nested JSON | 5,000,000 nested `[` is rejected in 5 ms (400). No stack exhaustion. (The response size is SEC-S02-6.) |
| Drive-by browser requests | The decoder requires `Content-Type: application/json`, which a cross-site form cannot send without a CORS preflight, and no CORS headers are set. Do not add `Access-Control-Allow-Origin: *` when the frontend arrives. |
| Optimistic concurrency | `version` is required for `PUT`; a mismatch gives 409 by design. The update filter on `{_id, version}` (story) makes it atomic. |
| Error status semantics | 404 / 409 / 400 differences reveal no more than the open list endpoint does (accepted above). |
| Convention checks | Operation ids camelCase, one `catalog` tag, no explicit 4xx responses, `/api` prefix, `Error` shape. |

## Notes for the implementation review (not findings yet)

To check when the adapters exist (proposal 6):
- The unmapped-error path: any repository error that is not `ErrNotFound`, `ErrDuplicateISBN` or `ErrVersionConflict` must reach `handler.NewError` (500, generic), never the response. E11000 on `isbn` must map to 409 without echoing the driver text.
- Every MongoDB call uses a context with a timeout (`ReadTimeout` of the server does not cancel driver calls).
- `total` for an unfiltered list: `CountDocuments({})` is O(n); `EstimatedDocumentCount` is O(1) but not exact. Pick deliberately.
- No logging of request bodies. ogen's `DecodeBodyError` keeps the raw body in its detail format; `slog` of the error with `%+v` would write it.
- Seeding (`SEED_DEMO_DATA`) is off by default and goes through the use cases; it is not reachable from the API.

## Follow-up tasks

Proposed in `.orch-proposals.json` (not committed; a human approves them):

1. **api / haiku**: spec changes for SEC-S02-4, 5, 8 (`additionalProperties: false`, `offset` maximum, `author` maxLength, `version` minimum, error descriptions).
2. **backend / sonnet**: body limit 64 KiB with 413, generic transport errors, security headers (SEC-S02-1, 6).
3. **backend / haiku**: server timeouts and `MaxHeaderBytes` (SEC-S02-2).
4. **backend / sonnet**: rate limiting middleware (SEC-S02-3). Depends on 2 (same files).
5. **backend / sonnet**: domain input hygiene (SEC-S02-7).
6. **security / sonnet**: implementation review and `security_books_test.go` regression tests, after the adapters and proposals 1–5 have landed.

My scope for this task was the report only, so no regression tests were written here. They are part of proposal 6 and need the fixes first; writing them now would only add skipped tests that proposals 1–3 would have to un-skip in a file they have no scope for.
