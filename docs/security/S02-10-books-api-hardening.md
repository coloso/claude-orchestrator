# S02-10: Hardening review of the books API, the MongoDB repository and demo seeding (S-02)

Reviewed with the books API wired and stored. Date: 2026-10-25. Reviewer role: security.

**Result: no critical, high or medium finding.** Every medium finding of the two earlier reviews is fixed. This review found three new low findings (SEC-S02-15, 16, 17) and one remaining low item from before (SEC-S02-13, image digests). Two of the new findings are pinned by skipped tests; one infrastructure gap was fixed directly in `docker-compose.yml`.

## Scope reviewed

- `backend/api/openapi.yaml` (catalog operations) and `internal/server` (body limit, security headers, transport errors).
- `internal/catalog`: `domain` (ISBN, Book, validation, paging), `app` (use cases, `SeedDemo`), `adapter/httpapi`, `adapter/memory`, `adapter/mongodb` (repository, counters document, migration `catalog/0001`).
- Wiring: `internal/bootstrap` (`SEED_DEMO_DATA`, seeding after the migrations, logging), `cmd/server`, `internal/handler` (`NewError`), `internal/platform/mongodb`.
- `docker-compose.yml`, `backend/Dockerfile`, `backend/.dockerignore`.
- Earlier reviews, read first and not repeated: [S02-02-books-api-spec.md](S02-02-books-api-spec.md), [S02-02.6-books-implementation.md](S02-02.6-books-implementation.md). Their status is in "Status of earlier findings".
- Not reviewed: the frontend (does not exist), `orchestration/`, `backlog/`.

### How this was verified

- **Code reading** of everything above, in the state of commit `bb6a7dd`.
- **Probes** against the wired app on memory storage (a throwaway test, deleted). It showed that invisible-only titles and authors are accepted (SEC-S02-15) and that a filter with invalid UTF-8 is accepted and answered with an empty list on memory.
- **Regression tests** in `backend/e2e/security_catalog_test.go` (see "Regression tests"). The two tests that pin a missing behavior were run un-skipped to confirm they fail today, then re-skipped. All memory variants pass, also with `-race`.
- `go tool govulncheck ./...`: no vulnerability in called code or imported packages. One module-level advisory (the `x/crypto` dependency of the MongoDB driver; nothing imports the affected package). `go tool gosec`: no issues.
- **Not verified:** the MongoDB variants of the new tests (`mongodb` sub-tests and the five Mongo-only tests) were written but **not run**. The environment did not allow me to start them against the local MongoDB (they need `MONGO_TEST_URI`), and no `mongod` is installed. They skip without it, so `backend/check.sh` is green, but `make test-mongo` should be run once and any failure treated as a real result. Likewise `docker compose config` was not run, so the compose edit below is checked as text only (by the new container-files test), not by Docker.

## Accepted risks

| Risk | Why accepted |
|---|---|
| **No authentication or authorization, including no authentication or TLS on MongoDB for local development** (stated once here) | The demo has no users and the stack is local. Anyone who can reach port 8000 can create, change and delete any book; anyone who can reach port 27017 can do anything to the database. Both ports are published on `127.0.0.1` only (pinned by `TestSecurityCatalogContainerFilesStayLockedDown`). This becomes a high finding the moment the stack is deployed anywhere else. |
| Plain HTTP | Local demo. |
| Sequential IDs and ISBNs can be enumerated | The list endpoint is open, so a 404 or 409 reveals nothing the list does not. Never-used and deleted IDs give the same 404 (pinned). |
| Hard delete, no audit trail; no cap on the number of books | Product decision. The rate limiter bounds the growth rate per client (5 writes/s, burst 20), not the total. With the default `STORAGE=memory` the same applies to process memory. |
| Rate-limit evasion by many source addresses; one shared bucket behind a proxy | A per-address limiter cannot do more. `X-Forwarded-For` is ignored on purpose. |

## Threat model

**Assets**: availability of the API and MongoDB (CPU, memory, connections, disk); integrity of the catalog (ISBN uniqueness, ID and version counters, `$jsonSchema` invariants); the process (no crash, no internals in responses or logs).

**Entry points**: `POST/GET /api/books`, `GET/PUT/DELETE /api/books/{id}`, `GET /api/health`. Untrusted: JSON bodies, the query parameters `author`, `limit`, `offset`, the path `id`, `Content-Type`, body length and speed, source address. Operator-controlled: the environment (`STORAGE`, `MONGO_URI`, `MONGO_DB`, `SEED_DEMO_DATA`, `PORT`), `docker-compose.yml`.

**Trust boundaries**: network → server middleware (headers, rate limit, 64 KiB body cap) → ogen decoder (types, unknown fields) → HTTP adapter → domain (the real validation) → repository → MongoDB (second line: `$jsonSchema`, unique `isbn_unique`).

| STRIDE | Threat | Result |
|---|---|---|
| **S**poofing | Out of scope (no auth). | Accepted. |
| **T**ampering | Mass assignment; operator injection through a string, an array, an object or the `author` filter; lost updates; duplicate ISBN in a different spelling; ID reuse; counter corruption by a crash. | All pinned and held, except that the filter skips the text rules (SEC-S02-16). |
| **R**epudiation | No audit log of writes. | Accepted with the auth risk. |
| **I**nformation disclosure | Driver, index or key text (E11000), ogen text, Go types, request bodies and control characters in logs; `Server` internals. | Nothing found. Pinned: exact bodies for 400/404/409/500; log lines cannot be forged. |
| **D**enial of service | Big or slow bodies, huge arrays, nesting, deep `skip`, big filter value, stalled database, concurrent seeding. | Bounded (see answers below). Nothing open except the accepted growth. |
| **E**levation of privilege | A compromised process in the container; a database user with more rights than needed. | Container restricted now; least-privilege user recommended for later. |

## Status of earlier findings

| ID | Title | Status | Evidence |
|---|---|---|---|
| SEC-S02-1 | Unbounded request body | **Fixed** | `server.go:23` 64 KiB, `limitBody` (`server.go:89`) + `*http.MaxBytesError` → 413. Pinned in `security_books_test.go`. |
| SEC-S02-2 | Server timeouts incomplete | **Fixed** | `cmd/server/main.go:73`: header 5 s, read 10 s, write 15 s, idle 60 s, headers 16 KiB. |
| SEC-S02-3 | No rate limiting | **Fixed** (accepted limits above) | `internal/middleware/ratelimit.go`; `security_ratelimit_test.go`. |
| SEC-S02-4 | Unknown properties ignored | **Fixed** | `additionalProperties: false` on `BookInput`/`BookUpdate`. |
| SEC-S02-5 | `offset` unbounded | **Fixed** | `maximum: 100000`. |
| SEC-S02-6 | Transport errors and headers | **Fixed** | Fixed texts in `writeError`; three headers on every response. |
| SEC-S02-7 | Unicode and control characters | **Fixed, one gap left** | `isPlainText`, ASCII-only ISBN digits. Invisible-only text still passes: SEC-S02-15. |
| SEC-S02-8 | Spec hygiene | **Fixed** | `version` minimum 1, `author` maxLength 200, error docs. |
| SEC-S02-9 | No deadline on MongoDB operations | **Fixed** | `repository.go:22,28`: 5 s per repository call, derived into `maxTimeMS` by the driver. `TestSecurityBooksMongoOperationsHaveATimeout` is active. |
| SEC-S02-10 | Unfiltered `total` is an O(n) count | **Closed by decision** | `repository.go:156`: exact `CountDocuments` is deliberate, documented, bounded by the 5 s deadline. |
| SEC-S02-11 | MongoDB without authentication or TLS | **Accepted for local dev** (see above) | A finding for any other deployment. |
| SEC-S02-12 | IPv6 rate-limit evasion; health endpoint | **Fixed** | `ratelimit.go:180` keys IPv6 by /64; `handler.go` caches the ping for 1 s. |
| SEC-S02-13 | Container hardening | **Mostly fixed here**; digests open | Compose edit below; digest pinning needs registry access: SEC-S02-13 remainder, proposal 4. |
| SEC-S02-14 | Duplicate ISBN detected by message text | **Fixed** | `repository.go:230,247` read `keyPattern` of the write error, not the message. A duplicate `_id` stays a 500 on purpose. |

## Findings

| ID | Title | Severity | Location | Impact | Recommendation | Follow-up |
|---|---|---|---|---|---|---|
| SEC-S02-15 | A title or author made only of invisible characters is accepted | low | `domain/book.go:138` (`normalizeTitle`), `:149` (`normalizeAuthors`) | `POST {"title":"<U+200B>"}` returns 201. The catalog gets books with a blank-looking title and authors that cannot be told apart from each other or found with the filter. The "empty after trimming" rule does not hold for format characters. | After trimming, require one character that is neither white space nor category Cf. Keep joiners inside real text valid (emoji sequences). | Proposal 1; `TestSecurityCatalogTextWithoutAVisibleCharacterIsRejected` (skipped) |
| SEC-S02-16 | The `author` filter skips the text rules | low | `app/service.go` `ListBooks` → `domain.ListFilter{Author: ...}` (`domain/page.go:25`) → `mongodb/repository.go:156` | Invalid UTF-8, NUL and control characters go straight into a BSON filter. No stored name can contain them, so the answer is always empty, but the database receives a value the API never allows elsewhere. How a MongoDB server reacts to an invalid UTF-8 string in a filter was not verified here; the worst case is a generic 500 (no leak). | Validate the filter with the same `isPlainText` rule and answer 400 naming `author`. | Proposal 2; `TestSecurityCatalogAuthorFilterFollowsTheTextRules` (skipped) |
| SEC-S02-17 | Outside Docker the server listens on every interface | low | `cmd/server/main.go:33` (`fmt.Sprintf(":%d", port)`) | `make backend` (`go run ./cmd/server`) exposes an API without authentication, backed by in-memory storage by default, to the whole network. Compose already binds the published port to `127.0.0.1`, so only the non-Docker path is affected. | `LISTEN_HOST`, default `127.0.0.1`; the Dockerfile sets `0.0.0.0` because the published port is loopback-only. | Proposal 3 |
| SEC-S02-13 (rest) | Base images are tags, not digests | low | `docker-compose.yml:5` (`mongo:8.0`), `backend/Dockerfile:2,10` | A moved tag changes what runs without a change in the repository. | Pin by digest. Needs `docker buildx imagetools inspect`, which I could not run. | Proposal 4 |

### SEC-S02-15 in more detail

Observed on memory storage (the check is in the domain, so MongoDB behaves the same, because its `minLength` counts code points and accepts one):

| Title or author | Result today |
|---|---|
| U+200B zero width space, U+200C, U+200D, U+2060 word joiner, U+FEFF, U+00AD soft hyphen, U+200E, U+202E, or several mixed with spaces | **201**, stored as sent |
| U+00A0 no-break space | 400 (`strings.TrimSpace` trims it) |
| U+0085, U+007F, NUL, newline | 400 (`unicode.IsControl`) |

`unicode.IsControl` is category Cc and `TrimSpace` uses `unicode.IsSpace`; format characters (Cf) and the line/paragraph separators U+2028/2029 inside text are neither. Notes, not findings:

- U+2800 (braille blank), U+3164 (Hangul filler) and look-alike letters also look empty or identical; they are letters or symbols, so no rule on character classes can reject them. The fix above is deliberately limited to Cf and white space.
- Bidi controls (U+202A–202E, U+2066–2069) inside otherwise normal text are accepted and are legal in the existing tests. A frontend that shows titles must isolate them (`<bdi>`); that is a decision for the frontend story.
- **Unicode normalization:** nothing normalizes, so "Gödel" in NFC and in NFD are two different titles and two different authors; the exact-match filter finds only the spelling it is given. Duplicate-looking titles are allowed (only the ISBN is unique). Note only; normalizing would change stored text.

### SEC-S02-16 in more detail

`ListBooks` validates `limit` and `offset` (`domain.NewPage`) but not `author`. The OpenAPI `maxLength: 200` bounds its size (the bound counts characters, pinned with a 200-character name of two-byte letters), and ogen guarantees it is a string, so **no operator can be injected** (see below). What is missing is the text rule. Probe on memory: `?author=%ff`, `?author=a%00b`, `?author=Alice%0a` all answer `200 {"items":[],"total":0}`.

### SEC-S02-17 in more detail

`run(fmt.Sprintf(":%d", port))` binds all interfaces. The accepted "no authentication" risk was reasoned for a local demo; binding every interface by default quietly widens "local" to "everyone on the network". Existing behavior that must keep working: the container health check dials `127.0.0.1:PORT`, and the e2e tests use `httptest`, so neither depends on the bind address.

## Answers to the review questions

### Input handling and resource limits

| Question | Answer |
|---|---|
| Body size limit? | **Yes, 64 KiB** (`server.go:23`), checked on `Content-Length` and, for chunked bodies, with `http.MaxBytesReader`; both give 413 with a fixed text. Pinned (`security_books_test.go`). The largest legal book is about 52 KB even in escape form. |
| Which server timeouts? | `cmd/server/main.go:73`: `ReadHeaderTimeout` 5 s, `ReadTimeout` 10 s, `WriteTimeout` 15 s, `IdleTimeout` 60 s, `MaxHeaderBytes` 16 KiB. None is missing. A handler's context is not cancelled by `WriteTimeout`; the repository's own 5 s deadline covers that. The longest request (update: one `Get`, one `ReplaceOne`) is 2 × 5 s, below the 15 s write timeout. |
| Huge `authors` arrays or strings decoded before the domain rejects them? | Bounded by the 64 KiB cap: 64 KiB of `""` is about 21,000 empty strings, a few hundred KB. Decoding therefore cannot be pushed past the body size; the domain then answers 400. Pinned with a 2,000,000-entry array (413) and 21 authors / 301 characters / 201-character names (400). |
| Deep offsets? | `offset ≤ 100000` in the spec (400 above). The cost of `skip` is bounded by that and by the 5 s deadline plus `maxTimeMS`. `GET /api/books?limit=100&offset=100000&author=Alice` is pinned to answer within 3 s. With an `author` filter the planner may walk `_id` or sort the `authors_lookup` matches; both are bounded by collection size, which the rate limiter bounds per client (accepted). |
| Large `author` values? | `maxLength: 200` characters in the spec; longer is 400 (pinned). A header limit of 16 KiB bounds the query string anyway. The value is compared by equality, never as a pattern. |

### Validation

| Question | Answer |
|---|---|
| ISBN accepts only ASCII digits? | **Yes.** `ParseISBN` tests `r >= '0' && r <= '9'`, and only `-` and ` ` are separators. Pinned with 29 spellings: Arabic-Indic, extended Arabic-Indic, Devanagari, full-width, mathematical bold and double-struck, subscript digits; no-break and thin space, ideographic space, Unicode hyphens and dashes, minus sign, tab, newline, underscore, dot, slash, byte order mark, `ISBN ` prefix, ISBN-10, wrong length; each is 400 naming `isbn`, on POST and PUT. |
| Empty after trimming vs invisible characters? | Blank and NBSP-only are rejected; invisible-only is not: SEC-S02-15. |
| NFC/NFD, duplicate-looking titles | Note only (above). |
| Control characters and newlines in strings that reach logs? | Rejected in title, authors, ISBN; and the values that are logged (path and query values in transport errors, at DEBUG) are quoted by `slog`. Pinned: hostile `\r\n` plus a forged `time=2099 level=ERROR` line in an id, `limit`, `offset`, `author`, a body field and a body key never produces a line that does not start like a record. Title and author values are not logged at all except demo titles at DEBUG during seeding (fixed demo data). |

### NoSQL injection

- The only user-controlled value in a filter is `bson.D{{Key: "authors", Value: f.Author}}` with a Go `string` (`repository.go:156`); `_id`, `isbn` and `version` filters come from `int64`/`ISBN` values; writes go through the typed `bookDoc`. There is no `bson.M`, no `$where`, no raw command.
- **Request decoding cannot smuggle objects into strings.** ogen decodes into Go types: `{"$ne":""}`, `{}`, `["x"]`, `null`, numbers and booleans in `isbn`, `title`, `language`, `authors` (also as array items), `publishedYear`, `pages` and `version` all give 400 with a fixed text and store or change nothing. Differently cased keys (`ISBN`), `$`-prefixed or dotted keys and nested objects as extra keys give 400 (`additionalProperties: false`). A BOM, UTF-16, a second document, trailing bytes, a NUL byte and a duplicate key whose second value is an object also give 400. Pinned for POST and PUT.
- Query side: `author[$ne]=…`, `[$regex]`, `[$in][]`, `[$gt]`, `[$exists]`, `author.$ne=`, `$or[0][authors]=`, `$where=sleep(3000)` and a JSON document as the value are either ignored (full list) or compared as literal text (empty list). The test data makes any operator reading visible: `$ne Alice` would return Bob and Carol, `$regex Al` would return Alice. Authors named `{"$ne":""}`, `$where`, `.*` are found only by their exact name.

### Error leakage

- 500: `handler.NewError` answers `{"error":"internal server error"}` and logs the error; `mapError` returns unknown errors unchanged. New pins use errors the database itself produces: a non-numeric counter (TypeMismatch) and a counter behind the data (duplicate key on `_id`, whose message contains the index name and the key value). Both give the exact generic body (MongoDB variant, not run).
- 409 and 404 use the sentinel's own text, never the wrapped error: exact bodies are pinned (`a book with this ISBN already exists`, `version conflict: the book was changed since it was read`, `book not found`).
- 400 validation messages are `field: fixed reason`; a marker value put in `isbn`, `title`, `authors` and `language` never comes back. Transport errors (ogen) are replaced by fixed texts that keep only a parameter name.
- Logs: ogen errors are logged at DEBUG with `%v`, which carries field names and positions, not body values (pinned in `security_books_test.go`). A driver error in the ERROR log can contain document values when the server reports `Document failed validation`; the domain makes that unreachable, and `slog` quotes the text.

### Consistency

| Check | Result |
|---|---|
| Optimistic locking | `ReplaceOne` with the filter `{_id, version}` (`repository.go:193`); a miss is followed by a `Get` to tell 404 from 409. Concurrent updates: `TestStorageConcurrentUpdatesWithTheSameVersionAllowExactlyOne`. A rejected update (any 400) leaves the book unchanged (pinned). |
| Counter atomicity | `findOneAndUpdate` + `$inc` + upsert is atomic. 60 concurrent creates over 30 ISBNs on a fresh database: pinned to give exactly 30 × 201, 30 × 409, unique positive IDs (on MongoDB the very first calls race to create the counter; the retry in `nextID` covers that). |
| IDs never reused after a delete | The counter is independent of `books`. Pinned: after deleting the highest ID the next ID is greater. |
| Crash between counter and insert | Leaves a gap only. Pinned by moving the counter by 5 (what a dead instance leaves): next ID is +6, the gap IDs are 404. If the counter falls *behind* the data (restore, manual edit) every create fails with a generic 500 until the counter has passed the highest ID; each failed attempt moves it by one. That is fail-closed, nothing leaks (pinned). |
| Duplicate ISBN in another spelling | Normalization runs in the domain before the repository, so the unique index sees one form. Pinned: five spellings of one ISBN give one book; a PUT onto another book's ISBN in a different spelling is 409; a book may respell its own ISBN (200); a moved ISBN is free again. |
| Concurrent seeding by two instances | `SeedDemo` looks up each ISBN first and treats `ErrDuplicateISBN` as skipped, so racing instances converge on 12 books without an error; the loser's counter values become gaps. Test: 4 instances start at once, then 12 distinct ISBNs and 12 distinct IDs (MongoDB, not run). Migrations are serialized by the schema lock, not by the seeding. |

### Seeding

- **Off by default.** `ConfigFromEnv` uses `strconv.ParseBool`; only `1`, `t`, `T`, `TRUE`, `true`, `True` turn it on. Anything else (`yes`, `on`, `enabled`, `" true"`, a typo) means off, silently, which is the safe direction. Pinned for 13 values, and a zero `Config` serves an empty catalog.
- **On failure it stops the start.** `bootstrap.New` returns `seed demo data: …` and closes the connections; `cmd/server` exits 1. It does not serve a half-seeded catalog silently. Books added before the failure stay (each is its own insert, no transaction); the next start skips them and completes. Test: a foreign book occupies the ID the third demo book would get; start fails, 2 demo books + the foreign one exist, the restart ends with 12 + 1 and no duplicate ISBN (MongoDB, not run). With `restart: unless-stopped` a persistent failure is a crash loop, not a hazard.
- **Not triggerable over HTTP.** `SeedDemo` is called only from `bootstrap.New` (`bootstrap.go:139`) and is not in `UseCases`; pinned in `security_books_test.go` with guessed paths.
- **Nothing in the container files turns it on.** Compose has no `SEED_DEMO_DATA` (a comment now says so on purpose), the Dockerfile has no `ENV`/`ARG`. Pinned by `TestSecurityCatalogContainerFilesStayLockedDown` (the value, if ever set, must parse as false).
- The Info log is one summary line without titles, ISBNs or authors (pinned); titles appear only at DEBUG, and they are fixed demo data.

### ID enumeration

Accepted (see above). Sequential IDs from 1, no tombstones. The 404 text is identical for deleted and never-used IDs (pinned), so the only thing visible is what the open list shows anyway.

### MongoDB least privilege (recommendation for later)

The app currently connects as whatever `MONGO_URI` carries (nothing, locally). When authentication is turned on for a real deployment:

| Namespace | Actions needed at run time |
|---|---|
| `books` | `find`, `insert`, `update`, `remove` |
| `counters` | `find`, `update` **and `insert`** (an upsert that creates the document needs `insert` as well as `update`) |
| `schema_migrations`, `schema_lock` | `find`, `insert`, `update`, `remove` (the migrator, which runs in the same process at start-up) |
| the database | `createCollection`, `collMod`, `createIndex` (migrations only), and `listCollections`/`listIndexes` |

The last two rows are why one runtime user is broader than the five operations suggest. The clean split is a migrator user for a start-up/migration step and a runtime user with only the first two rows; that needs a code change (run migrations separately from serving) and is not proposed now. No `dbAdmin`, `readWriteAnyDatabase` or `root` role is needed.

## Infrastructure changes made in this task

`docker-compose.yml`, service `backend`:

- `read_only: true`, `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`, `mem_limit: 256m`, `pids_limit: 256`. The server writes nothing to disk, binds a port above 1024 and needs no capability. A compromised process loses the ability to write binaries, gain privileges or exhaust the host.
- Removed `CORS_ORIGIN: http://localhost:5173`: no code reads it, and its presence suggested that CORS is configured. The API sends no `Access-Control-*` header (pinned in `security_books_test.go`); an explicit origin, never `*`, belongs to the frontend story. `README.md:402` still lists it (out of my scope; part of proposal 3).
- A comment states that `SEED_DEMO_DATA` is deliberately not set.

`backend/Dockerfile` and `backend/.dockerignore` needed no change: distroless `static-debian13:nonroot`, `CGO_ENABLED=0`, no shell, no `ENV`/`ARG`, a `HEALTHCHECK`, and an allow-list `.dockerignore` that sends only `go.mod`, `go.sum`, `api/`, `cmd/`, `internal/` (no tests, no `.git`, no `.env`). The `mongo` service was left as it is: its image entrypoint drops privileges with `gosu`, so `cap_drop: ALL` would most likely break it, and the database needs a writable data volume. I could not test a stricter variant.

Not changed: image digests (SEC-S02-13 remainder, proposal 4). The restriction flags were checked as text only, not started with Docker (see "How this was verified"). That the server works with a read-only root filesystem and no capabilities is reasoned from the code (it writes no file and uses no `/tmp`), not tested; run `make up` once to confirm the container becomes healthy.

## Checked and fine

| Check | Result |
|---|---|
| Body, header and timeout limits | See answers above. |
| Method and path surface | `DELETE`/`PUT`/`PATCH`/`OPTIONS`/`TRACE` on the collection, `POST`/`PATCH`/`OPTIONS`/`TRACE` on an item, trailing and doubled slashes, encoded slashes and dots, `;x=1`, `%00`, other case, `*`, `all`, `export`, `/metrics`, `/debug/pprof/`, `/debug/vars` all answer 4xx with a short JSON error, and the stored book is untouched afterwards (pinned). There is no bulk operation and no debug endpoint. |
| Mass assignment | Pinned in `security_books_test.go`. |
| Response headers, CORS | `nosniff`, `no-store`, CSP on every response; no `Access-Control-*`. Pinned there. |
| Integer handling | `int64` for ids, versions and totals; `int` for year, pages, limit, offset; 20-digit and 2^63 values give 400. |
| Domain after trimming | Counted in code points (`utf8.RuneCountInString`), as `$jsonSchema` counts them. Language check is anchored ASCII, so the PCRE `$`-before-newline quirk of the database validator cannot be used. |
| MongoDB adapter | Every call has the 5 s deadline; list size ≤ 100; `skip` ≤ 100000; fixed `_id` sort; `cur.All` closes the cursor; `bookDoc` is the only write shape and matches the validator. |
| Unmapped errors | Generic 500 on every route. |
| `govulncheck`, `gosec` | Clean (see above). |
| Secrets | None in the repository, the image or compose. Only `storage` is logged at start-up, not the URI. Pinned with a pattern check. |
| Ports | Published on `127.0.0.1` only (both services). Pinned. |
| Image | Non-root distroless; `.dockerignore` allow-list; health checks on both services. Pinned. |

## Regression tests

`backend/e2e/security_catalog_test.go` (package `e2e_test`, every helper prefixed `securityCatalog`). Each test runs on memory and on MongoDB (database `test_<random hex>`, dropped in `t.Cleanup`) unless noted; the MongoDB variants skip without `MONGO_TEST_URI`.

| Test | Pins | State |
|---|---|---|
| `…JSONOfTheWrongShapeIsRejected` | Objects, arrays, null, numbers and booleans in every field (POST and PUT, `version` too), odd keys, BOM, UTF-16, trailing data → 400, fixed text, nothing stored or changed | active |
| `…AuthorFilterNeverBecomesAnOperator` | 20 operator-shaped queries: rejected, ignored, or literal; never a subset an operator would give; no slow `$where` | active |
| `…OperatorLookingAuthorNamesStayText` | Names like `{"$ne":""}`, `$where`, `.*` are found only by exact name | active |
| `…OddAuthorFiltersAreNeverAServerError` | Percent-encoded operators, 200-character filters (two- and four-byte), empty, deepest offset; the 200-character name is findable | active |
| `…AuthorFilterFollowsTheTextRules` | Invalid UTF-8, overlong UTF-8, NUL, newline, DEL in `author` → 400 naming `author` | **skipped: SEC-S02-16** |
| `…TextWithoutAVisibleCharacterIsRejected` | Invisible-only title/author → 400 on POST and PUT; emoji sequence with a joiner stays valid | **skipped: SEC-S02-15** |
| `…ISBNAcceptsOnlyASCIIDigitsHyphensAndSpaces` | 29 non-ASCII or malformed spellings → 400 `isbn:`; five valid spellings = one canonical book | active |
| `…OneBookHasOneISBNWhateverTheSpelling` | PUT onto another ISBN in another spelling is 409; respelling its own is 200; a moved ISBN is free | active |
| `…ErrorsAreFixedTextsThatNeverEchoInput` | Exact 409/404 bodies; same 404 for deleted and unused IDs; validation messages do not echo values | active |
| `…DatabaseErrorsBecomeAGeneric500` | Server-produced errors (TypeMismatch, duplicate `_id`) → exact generic body | active, MongoDB only, **not run** |
| `…ConcurrentCreatesKeepIDsUniqueAndISBNsOnce` | 60 racing creates; unique IDs; removed IDs not reused | active |
| `…ACrashAfterTheCounterOnlyLeavesAGap` | Counter moved by 5 → next ID +6; gap IDs 404 | active, MongoDB only, **not run** |
| `…DemoSeedingIsOptIn` | 13 values of `SEED_DEMO_DATA`; zero `Config` is empty | active |
| `…SeedingLogsOnlyASummaryAtInfo` | One summary line, no titles/ISBNs/authors | active |
| `…ConcurrentSeedingConvergesOnOneDemoCatalog` | 4 instances at once → 12 books, unique ISBNs and IDs | active, MongoDB only, **not run** |
| `…SeedFailureStopsStartupAndARestartCompletes` | Start-up error, partial catalog, restart completes without duplicates | active, MongoDB only, **not run** |
| `…HostileValuesCannotForgeLogLines` | CR/LF and a forged record in ids, query values, body fields and keys | active |
| `…OnlyTheDocumentedOperationsExist` | 29 method/path variants → 4xx, short JSON, book untouched | active |
| `…ContainerFilesStayLockedDown` | Seeding off, loopback ports, no secrets, restricted backend container, non-root distroless image, allow-list build context | active |

## Follow-up tasks

Proposed in `.orch-proposals.json` (not committed; a human approves them):

1. **backend / haiku**: reject titles and authors without a visible character (SEC-S02-15). Un-skips `…TextWithoutAVisibleCharacterIsRejected`.
2. **backend / haiku**: apply the text rules to the `author` filter (SEC-S02-16). Depends on 1 (same domain file). Un-skips `…AuthorFilterFollowsTheTextRules`.
3. **backend / haiku**: `LISTEN_HOST`, default loopback; Dockerfile sets `0.0.0.0`; README configuration list without `CORS_ORIGIN` (SEC-S02-17).
4. **security / haiku**: pin images by digest (SEC-S02-13 remainder). Depends on 3 (same Dockerfile).

Not proposed: MongoDB authentication and a least-privilege user (accepted for local dev above; the privilege table is the input for that later task).
