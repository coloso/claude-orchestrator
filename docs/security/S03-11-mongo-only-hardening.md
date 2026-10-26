# S03-11: Hardening review of the MongoDB-only storage (S-03)

Reviewed with story S-03 merged on branch `agent/S03-11` (diff against `main`: 32 files, memory adapter removed, bootstrap, tests, docs and tooling changed). Date: 2026-10-26. Reviewer role: security.

**Result: no critical, high or medium finding.** Start-up fails closed, no second storage path is left, and no security regression test lost an assertion. Three new low findings: two ways in which a failing start-up can still print parts of `MONGO_URI` (SEC-S03-1, SEC-S03-2; pinned by skipped tests, one follow-up proposed) and one in a test failure message (SEC-S03-3; fixed directly in `security_books_test.go`). Defaults and shared-server use are operator risks, described below, not findings.

## Scope reviewed

- The story diff against `main`, read in full for: `backend/internal/bootstrap/` (code and tests), `backend/cmd/server/` (code and tests), `backend/internal/platform/mongodb/mongodb.go`, `backend/check.sh`, `Makefile`, `docker-compose.yml`, `backend/e2e/e2e_test.go` (the shared helper), `startup_test.go`, and all three `security_*_test.go` files.
- The current code for everything that handles `MONGO_URI`, `MONGO_TEST_URI`, `MONGO_DB` and `STORAGE`: `ConfigFromEnv`, `New`, `redactMongoURI`, `mongodb.Connect`/`Ping`, the migrator's logging, the health handler, every `Drop`, `Database(` and `bootstrap.New` call in `backend/**`.
- `backend/Dockerfile` and `backend/.dockerignore`: unchanged by the story (not in the diff); re-read.
- The MongoDB driver's URI parser (`go.mongodb.org/mongo-driver/v2@v2.9.2`, `x/mongo/driver/connstring`), for the error texts it can produce.
- Earlier reviews, not repeated: [S02-02-books-api-spec.md](S02-02-books-api-spec.md), [S02-02.6-books-implementation.md](S02-02.6-books-implementation.md), [S02-10-books-api-hardening.md](S02-10-books-api-hardening.md).
- Not reviewed: `orchestration/`, `backlog/`, the frontend (does not exist), the API surface itself (unchanged by S-03; S02-10 stands).

### How this was verified

- **Code reading** of the above.
- **Probes** (a throwaway test in `backend/e2e`, deleted): 19 hostile or malformed `MONGO_URI` values run through `bootstrap.New`, printing the error. They confirmed SEC-S03-1 and SEC-S03-2 and showed which URIs are safe (see "Credentials").
- **Regression tests** in `backend/e2e/security_startup_test.go`. The two skipped tests were run un-skipped to confirm they fail today (4 of 5 and 2 of 2 rows fail), then re-skipped. The other new tests pass.
- `backend/check.sh` with `MONGO_TEST_URI` set (a MongoDB reachable from this environment): **passes**, including the whole e2e suite on MongoDB, gosec (no issues) and govulncheck (no vulnerability in called code or imported packages; one module-level advisory in a required module that is not called, as in S02-10; I could not run `govulncheck -show verbose` here, so I did not re-identify it).
- **Not verified:** `docker compose config` / `up` (no docker); the effect of a MongoDB with authentication on any error text (the test server has none); `MONGO_TEST_URI` values with credentials or a `mongodb+srv` scheme (not available here).

## Accepted risks

| Risk | Why accepted |
|---|---|
| **No authentication or authorization in the API** (stated once here) | The demo has no users. Anyone who can reach port 8000 can create, change and delete any book. |
| **No authentication and no TLS on MongoDB** | Local development: compose runs `mongo` without `--auth` and publishes it on `127.0.0.1` only (pinned by `TestSecurityCatalogContainerFilesStayLockedDown`). Anyone on the host can read and change the data, including database `orch`. Out of scope of this review; a finding for any other deployment. Note for that day: `MONGO_URI` has no `tls=true` by default, and the code does not require it. |
| Plain HTTP, sequential IDs, hard delete, no cap on the number of books, rate-limit evasion by many addresses | Unchanged since [S02-10](S02-10-books-api-hardening.md). The note "with `STORAGE=memory` the same applies to process memory" is obsolete: growth now goes to disk. |
| Running the test suite against a shared or production MongoDB | An operator risk, see "Test isolation". |
| The default `MONGO_URI` points at whatever listens on `localhost:27017` | An operator risk, see "Defaults". |

## Threat model

**Assets**: confidentiality of the MongoDB credentials (a `MONGO_URI` or `MONGO_TEST_URI` that carries them); integrity of any database the process or the tests connect to (`orch`, and every other database on that server); availability (the server must not serve a half-working API).

**Entry points** (changed by S-03): the environment (`MONGO_URI`, `MONGO_DB`, `STORAGE`, `SEED_DEMO_DATA`), `MONGO_TEST_URI` for the tests and `check.sh`, the compose file, the Makefile targets `backend`, `mongo`, `check`. The HTTP API is unchanged.

**Trust boundaries**: operator environment → `bootstrap.ConfigFromEnv` → `bootstrap.New` (validation, redaction) → driver (URI parser, network) → MongoDB; process output (logs, `go test` output, CI logs) → whoever reads them; test process → shared MongoDB server.

| STRIDE | Threat | Result |
|---|---|---|
| **S**poofing | A different server answers on the default `localhost:27017`; no TLS or auth. | Accepted / operator risk (above). |
| **T**ampering | A test or a start-up writes into a database it does not own (`orch`, a production database). | Tests: pinned to `test_<random hex>` databases. Start-up: writes only where `MONGO_URI`/`MONGO_DB` point; see "Defaults". |
| **R**epudiation | No audit log. | Accepted with the auth risk. |
| **I**nformation disclosure | Credentials from `MONGO_URI` or `MONGO_TEST_URI` in an error, log, test failure or CI output. | Mostly held; three narrow leaks: SEC-S03-1, 2, 3. |
| **D**enial of service | Start-up hangs without MongoDB; half-started server; database outage while serving. | Held: bounded start-up, no serving without MongoDB; outage gives 503/500 per request, no crash. |
| **E**levation of privilege | Fallback to a weaker storage when MongoDB is missing; `STORAGE` switching behavior. | Held: no fallback exists (see 1). |

## Findings

| ID | Title | Severity | Location | Impact | Recommendation | Follow-up |
|---|---|---|---|---|---|---|
| SEC-S03-1 | `redactMongoURI` can print the user name and the start of the password | low | `backend/internal/bootstrap/bootstrap.go:83` (used at `:112`) | `net/url` splits a URI differently from the driver. With `MONGO_URI=mongodb://alice:12345#x@db:27017/` and an unreachable server, the error is `connect to MongoDB at mongodb://alice:12345: …` (user and the digits before the `#`/`?`); `mongodb://alice#x@db/` prints `mongodb://alice`; a URI without a scheme, `alice:pw@db:27017`, prints `alice:`. Needs a failing start-up, an unescaped special character (the driver accepts it, the MongoDB spec says to escape it) and someone reading the output. | Redact the way the driver parses: scheme must be `mongodb`/`mongodb+srv`; drop everything up to the first `@`; keep hosts and path. | Proposal 1; `TestSecurityStartupRedactionSurvivesUnescapedSpecialCharacters` (skipped) |
| SEC-S03-2 | The driver's URI parse errors, wrapped with `%w`, quote option values | low | `backend/internal/platform/mongodb/mongodb.go:19` (`mongodb connect: %w`), wrapped again at `bootstrap.go:112` | `…/?tlsCertificateKeyFilePassword=S3cr3t%zzPw` gives `error parsing uri: invalid option value "S3cr3t%zzPw": invalid URL escape "%zz"`: the whole secret value is printed. Typed options print their value too (`invalid value for "maxPoolSize": "…"`). Narrow: needs a secret in the query with a bad percent-escape (or in a numeric option) and a failing start-up. The same text reaches test output through `e2eTestDB` (`connect to MONGO_TEST_URI: %v`). | For errors raised while parsing the URI, return a fixed message without driver text; keep the driver text for network errors (`*net.DNSError`, `*net.OpError`). | Proposal 1; `TestSecurityStartupDriverParseErrorsDoNotEchoQueryValues` (skipped) |
| SEC-S03-3 | A test failure message printed the whole `MONGO_TEST_URI` | low | `backend/e2e/security_books_test.go:1030` (was `:1031`) | `TestSecurityBooksMongoOperationsHaveATimeout` did `t.Fatalf("cannot find the host of MONGO_TEST_URI: %v", err)`, and `url.Parse` errors quote their input (`parse "mongodb://user:pass word@…": net/url: invalid userinfo`). A `MONGO_TEST_URI` that the driver accepts but `net/url` rejects (a space, a bad escape) put the credentials into the test output and CI logs. | Never format the URI or a `url.Parse` error. | **Fixed here**: `secMongoHost` returns a fixed message; pinned by `TestSecurityStartupMongoTestHostErrorsNeverQuoteTheURI` (active) |

Not findings, but noted:

- **Auth-enabled test servers are not supported by one test.** `TestSecurityBooksMongoOperationsHaveATimeout` forwards through a proxy and builds `mongodb://<proxy>/?directConnection=true` from the host only, so it drops credentials and TLS options of `MONGO_TEST_URI`. Out of scope (MongoDB authentication); it fails closed (an authentication error), nothing leaks.
- `internal/archtest/arch_test.go:226-246` still lists rules for `todo/adapter/memory`. They are rules for an example context, not a code path, and the file must not be modified. Harmless.

### SEC-S03-1 and SEC-S03-2 in more detail

Probe results (`bootstrap.New`, unreachable or invalid server; the part after `at` is `redactMongoURI`, the rest is the wrapped driver text):

| `MONGO_URI` | Error text | Verdict |
|---|---|---|
| `mongodb://u:12345#abc@127.0.0.1:1/` | `connect to MongoDB at mongodb://u:12345: mongodb ping: …` | **leaks** user and password prefix (SEC-S03-1) |
| `mongodb://u:12345?abc@127.0.0.1:1/` | same | **leaks** (SEC-S03-1) |
| `mongodb://u#abc@127.0.0.1:1/` | `… at mongodb://u: …` | **leaks** the user |
| `u:s3cret@127.0.0.1:1` (no scheme) | `… at u:: … scheme must be "mongodb" or "mongodb+srv"` | **leaks** the user |
| `…/?tlsCertificateKeyFilePassword=s3%zzcret` | `… error parsing uri: invalid option value "s3%zzcret": invalid URL escape "%zz"` | **leaks** the value (SEC-S03-2) |
| `…/?maxPoolSize=s3cret` | `… invalid value for "maxPoolSize": "s3cret"` | **leaks** the value (SEC-S03-2) |
| `mongodb://u:pa%zzword@127.0.0.1:1/` | `(unparsable MongoDB URI): … invalid password: invalid URL escape "%zz"` | fine: three characters of the escape only |
| bad escape in the user, bad port, `@@`, `:` in the password, `http://` scheme, `authMechanism=<secret>`, empty `authSource`, `tls=maybe`, spaces in the host | no user, no password, no query value | fine (pinned: `TestSecurityStartupMalformedURIsDoNotLeakUserinfo`) |
| valid URI, unreachable server | `… at mongodb://127.0.0.1:1/: mongodb ping: server selection error: …, current topology: { … Addr: 127.0.0.1:1 … Last error: dial tcp 127.0.0.1:1: connect: connection refused }` | fine: addresses only |
| `mongodb+srv://…` unknown name | `… error parsing uri: lookup _mongodb._tcp.<name> on <resolver ip>:53: no such host` | fine: host name and resolver address, no credentials |

The userinfo itself is never printed for a *well-formed* URI, which is the case `bootstrap_test.go` and `startup_test.go` pin (credentials, query, fragment dropped; the server `127.0.0.1:1` named).

## Answers to the review questions

### 1. Startup fails closed

| Question | Answer |
|---|---|
| Can `bootstrap.New` return without MongoDB? | **No.** `bootstrap.go:99-118`: empty `MongoURI` or `MongoDB` → error; `mongodb.Connect` pings the primary (5 s server selection, 2 s ping) → error; `mongodb.Migrate` → error; on any failure after connecting `app.Close` disconnects and `New` returns `nil, err` (pinned: `app == nil` in `TestNewFailsWhenMongoIsUnreachable` and `TestStartupFailsInProcessWhenMongoDBIsUnreachable`). The handler is built only after the migrations; seeding failure also aborts. |
| Can `cmd/server` serve without it? | **No.** `run` calls `bootstrap.New` (`main.go:49`) before `ListenAndServe` (`:57`); an error returns to `main`, which logs and exits 1. `-healthcheck` only queries a running server. Pinned with the real binary: `TestStartupServerExitsWhenMongoDBIsUnreachable` (non-zero exit within 15 s, output names the server and leaks no credentials). |
| Is a fallback storage path left? | **No.** `internal/catalog/adapter/memory` is gone; `bootstrap.Config` has no storage field; no Go file outside `archtest` mentions `adapter/memory`; `STORAGE` appears in no code, compose, Dockerfile, `.dockerignore` or Makefile line. Pinned (new): `TestSecurityStartupNoFallbackStorageIsLeft`. `handler.Handler{}` without `Ping` reports healthy, but only `bootstrap.New` builds a handler, and it always sets `Ping`. |
| Empty `MongoURI`/`MongoDB` rejected? | **Yes** in `New` (`bootstrap.go:100`; `TestNewRequiresMongoURIAndDatabase`: empty URI, empty database, both). A whitespace-only or otherwise invalid value fails in the driver (scheme error) and also exits. `ConfigFromEnv` replaces an *empty environment value* with the default (documented), so an empty `MONGO_URI` is not an error there; in a container the default `localhost` is the container itself, so it still fails closed. |
| `STORAGE` ignored? | **Yes.** `ConfigFromEnv` reads three variables (`bootstrap.go:50-62`). Pinned three ways: `TestConfigFromEnvIgnoresStorage`, `TestRunFailsWhenMongoIsUnreachable` (`STORAGE=memory`, still exits), `TestStartupIgnoresStorageMemoryAndUsesMongoDB` (data lands in MongoDB). |
| After start-up, MongoDB goes away | The server keeps running; requests answer a generic 500 and the health check 503 `storage unavailable` (cached 1 s). Pinned in S02 tests. No fallback, no data served from anywhere else. |
| `check.sh` without a database | Refuses before any check (`check.sh:9-14`). New pin: `TestSecurityStartupCheckScriptRefusesWithoutMongoTestURI` (unset and empty). `make check` is different, see "Defaults". |

### 2. Credentials

| Path | Can it print `MONGO_URI` / `MONGO_TEST_URI`? |
|---|---|
| Start-up error (`bootstrap.go:112`) | Redacted URI plus the driver's error. Credentials, query and fragment are dropped for well-formed URIs. **Leaks in narrow cases: SEC-S03-1, SEC-S03-2.** |
| Listening log (`main.go:57`) | `addr` (the listen address) and `database` (the name) only. Pinned on a real server run: `TestSecurityStartupServerOutputNeverCarriesTheMongoURI` (a marker in the URI's query and the strings `mongodb://`, `mongodb+srv://` never appear in the output of start, request and SIGINT shutdown). |
| Migration logs | `schema migration applied` (id, description, duration) and `schema lock not released` (error of a delete). No URI, no host. The lock owner stored in the database is hostname plus random hex. |
| Migration, seeding and ping errors | `migrate database schema: %w`, `seed demo data: %w`, `mongodb ping: %w`, `health check failed` (WARN). They carry server addresses and per-server last errors, not the URI. Authentication errors from the driver read `(AuthenticationFailed) Authentication failed.` without user or password; not verified against a server with authentication. |
| `unmapped error` (ERROR log) | Driver errors of requests; connection addresses, never the URI. Document values were already discussed in S02-10. |
| Process environment | The URI is in the environment, not in `argv`, so `ps` does not show it. The e2e test that runs the real server strips every `MONGO_*` variable from the child (`startupCleanEnv`). |
| Test failure messages | `e2eTestDB`, bootstrap `newTestDB`: `connect to MONGO_TEST_URI: %v`, `connect to drop …: %v` print driver errors (same class as SEC-S03-2). The URI itself is never formatted, except SEC-S03-3 (fixed). The fatal messages for a missing variable print the variable name only. |
| `check.sh`, `Makefile` | `check.sh` prints static text. `make` echoes recipe lines unexpanded (`$${MONGO_TEST_URI:-…}`), so the value is not echoed. `docker-compose.yml` has no credentials (pinned: no `mongodb://…@`). |

**Can driver error text wrapped with `%w` contain credentials?** In two ways, both narrow. (a) The URI parser quotes option values: `invalid option value %q` and `invalid value for %q: %q` (`connstring.go:344`, `:412-708`). A secret in the query string such as `tlsCertificateKeyFilePassword` is printed when its percent-escape is wrong (errors about `authMechanismProperties` are fixed texts and echo nothing). (b) `invalid username/password: invalid URL escape "%zz"` prints three characters of the userinfo, which is not a leak in practice. Everything else the driver reports at start-up is a host address, a DNS name, a resolver address or a fixed phrase. See SEC-S03-2.

### 3. Defaults and exposure

- **`make backend`** runs `go run ./cmd/server` with `MONGO_URI` defaulting to `mongodb://localhost:27017` and `MONGO_DB` to `orch`. The API listens on `127.0.0.1` (`LISTEN_HOST` default, added after S02-10), so only the local machine reaches it.
- **Compose** publishes MongoDB as `127.0.0.1:27017` (not on other interfaces; pinned) and the API as `127.0.0.1:8000`. Inside the compose network the backend talks to `mongo:27017`, no credentials, no TLS.
- **Who can reach MongoDB with these defaults:** every local user and process on the host, and anything that can open a connection to the host's loopback (an SSH `-R` forward, a local proxy). No authentication: they can read, change and drop every database on that server, not only `orch`. Remote hosts cannot reach it; browsers cannot speak the wire protocol.
- **The default silently targets whatever answers on `localhost:27017`.** Before S-03 the default was memory. Now, if a developer has a tunnel to a shared or production MongoDB on that port, `make backend` connects to it and the migrations create `books`, `counters`, `schema_migrations`, `schema_lock` and the validator in database `orch`; with `SEED_DEMO_DATA=true` it also inserts the 12 demo books. Migrations are forward-only and idempotent and nothing is dropped, so the damage is limited to creating that database and its collections. **Operator risk, accepted**: set `MONGO_URI`/`MONGO_DB` explicitly when something other than the compose MongoDB listens on that port.
- **`MONGO_DB` is not validated.** `MONGO_DB=admin` (or `local`, `config`) makes the migrations create collections in a system database. Operator-controlled, same risk class.
- **Data now persists.** Memory storage vanished with the process; the compose volume `mongo-data` and a developer's local MongoDB keep demo and test data, unencrypted, until `docker compose down -v`.
- **`make check`** sets `MONGO_TEST_URI` to `mongodb://localhost:27017` when it is unset, so it also runs against whatever listens there (see 5). `backend/check.sh` itself has no default.

### 4. Security regression tests against `main`

Compared file by file with `git diff main` (the whitespace-insensitive diff for `security_catalog_test.go`, the full diff for `security_books_test.go` and `security_ratelimit_test.go`).

| Question | Answer |
|---|---|
| Is every secure behavior still pinned, now on MongoDB? | **Yes.** All 18 `TestSecurityBooks…` and 19 `TestSecurityCatalog…` tests of `main` exist under the same names (plus `TestRateLimitAppliesToLoopbackAndSparesHealth`). The `…EachBackend` wrappers lost one level of nesting; the bodies are unchanged except for the harness calls. The assertions in the removed `memory` sub-tests were the same as in the `mongodb` sub-tests, which remain. |
| Was an assertion weakened? | **No.** In the diff, every removed assertion line comes back unchanged (re-indented). The only changed assertion behavior is stricter: Mongo-only tests that did `t.Skip("MONGO_TEST_URI is not set")` now fail without it (`e2eMongoURI`), and `t.Skipf("cannot find the host…")` became a failure. |
| `t.Skip("SECURITY <id>: …")` pins unchanged? | **There are none, before or after.** On `main` the S02-10 pins (`…AuthorFilterFollowsTheTextRules`, `…TextWithoutAVisibleCharacterIsRejected`) are already active, and are active in this branch. The only `t.Skip` calls in the `security_*` files on `main` were the missing-`MONGO_TEST_URI` skips, all removed (the other e2e files had the same kind of skip and were converted by the story; they are not security tests). The new skipped tests of this review are listed below. |
| Memory-only checks that went away | (1) The `memory` sub-test of each of the 24 tests (13 books, 11 catalog) that ran through `secEachBackend`/`securityCatalogEach`: each repeated the assertions of its `mongodb` sub-test on the other repository, so it only compared memory with MongoDB. (2) The helper fallback `if cfg.RateLimit.MaxClients == 0 { generous }`: every caller now gets generous limits from `e2eMongoConfig` or sets its own, so no test gets different limits. (3) `TestSecurityCatalogDemoSeedingIsOptIn` served `bootstrap.Config{}` (memory) and expected an empty catalog. It now serves `e2eMongoConfig` and also asserts `SeedDemoData` is false and the catalog empty, so "no seeding without opting in" is still pinned; that a zero `Config` starts at all is gone *by design*, and its replacement is `TestNewRequiresMongoURIAndDatabase`. |
| Tests that ran only on memory and now run on MongoDB | 413, deeply nested bodies, CORS, log forging, seeding log, rate limit and several more used `secStart(t, false)`/memory. They now also exercise the MongoDB path; they pass. |

### 5. Test isolation

- **Where databases come from.** `e2eTestDB` (e2e), `newTestDB` (bootstrap), and the two `testdb_test.go` helpers (`catalog/adapter/mongodb`, `platform/mongodb`) each build `"test_" + hex(8 bytes from crypto/rand)` (`test_` plus 16 hex digits) and drop exactly that database in `t.Cleanup`. A failed drop fails the test (`t.Errorf`). Cleanups run after the app and server are closed.
- **Nothing else drops.** `.Drop(` appears only in those four helpers; no `DropDatabase`; every `.Database("…")` literal in the repository starts with `test_`. New pin: `TestSecurityStartupTestsOnlyTouchRandomTestDatabases` fails on a new `Drop` site or a non-`test_` literal, and checks the name format and uniqueness at run time.
- **The name `orch`** appears in tests only as `MongoDB: "orch"` paired with a URI that refuses connections (`127.0.0.1:1`: `TestNewFailsWhenMongoIsUnreachable`, `TestStartupFailsInProcess…`, `TestRunFailsWhenMongoIsUnreachable`) and in `ConfigFromEnv` default checks that never connect. Tests that call `ConfigFromEnv()` and then use more than `SeedDemoData` set `MONGO_URI` and `MONGO_DB` first (`TestStartupIgnoresStorageMemoryAndUsesMongoDB`), so a developer's exported `MONGO_URI`/`MONGO_DB` cannot redirect a test to `orch`. The test that starts the real server strips every `MONGO_*` variable from the child and passes its own.
- **Operator risk, stated plainly.** `MONGO_TEST_URI` decides which server receives the tests. Pointing it at a shared or production server means: `test_*` databases with collections, indexes and validators are created and dropped there (one per test that needs storage, many in parallel), the server carries that load, and a run that is killed (`SIGKILL`, a `go test` timeout panic, a lost connection) leaves its `test_*` databases behind, because `t.Cleanup` does not run; nothing sweeps them. The tests never write outside `test_*`, but they also never check that the server is a disposable one. Use a throwaway MongoDB (`make mongo`). Parallel agents sharing one server are safe against each other for the same reason (separate databases), not against load.
- `make check` defaulting to `localhost:27017` is the same risk with a convenient default (see 3).

### 6. docker-compose.yml, Dockerfile, .dockerignore

- `git diff main -- docker-compose.yml`: one line removed, `STORAGE: mongo`. Nothing else changed.
- `backend/Dockerfile` and `backend/.dockerignore` are not in the diff.
- Still in place, and pinned by `TestSecurityCatalogContainerFilesStayLockedDown` (passes): `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges`, `mem_limit`, `pids_limit`; ports on `127.0.0.1`; no credentials; distroless `:nonroot` final image with a `HEALTHCHECK`; allow-list build context; seeding off. Base images are pinned by digest and `LISTEN_HOST` exists, so SEC-S02-13 (rest) and SEC-S02-17 of S02-10 appear fixed on `main`; I read this, I did not re-review it.
- Unchanged and out of scope: the `mongo` service has no `--auth`, no `read_only`/`cap_drop` (S02-10 explains why), a named volume, a health check.
- New pin: `TestSecurityStartupNoFallbackStorageIsLeft` fails if `STORAGE` returns to compose, Dockerfile, `.dockerignore` or Makefile.

### 7. Earlier reports

`docs/security/S02-02.6-books-implementation.md` and `S02-10-books-api-hardening.md` still describe the memory storage (`STORAGE=memory` as the default, "Both variants" tests, a probe "on memory storage", `adapter/memory` in scope, "only `storage` is logged at start-up"; S02-10 also records its MongoDB variants as not run). They are historical records of what was true when they were written, so they are **not rewritten**. Read them with this report beside them: the storage is MongoDB only; the start-up log line now carries `database`, not `storage`; the MongoDB variants and Mongo-only tests ran in this review (`backend/check.sh` passed).

## Checked and fine

| Check | Result |
|---|---|
| Start-up order | Connect → ping → migrate → (seed) → build handler → listen. Cleanup disconnects on every failure path. |
| Bounded start-up | 5 s server selection and 2 s ping; the unreachable-server tests assert failure within 10–15 s. |
| `ConfigFromEnv` | Three variables; `SEED_DEMO_DATA` still `ParseBool`, off for anything else. |
| `STORAGE` | Ignored in code, tests, compose, Makefile. |
| Listening address | `127.0.0.1` by default; the image sets `0.0.0.0` and compose publishes loopback only. |
| Secrets in the repository | None: README, CLAUDE.md, Makefile, compose and docs hold only `mongodb://localhost:27017` and `mongodb://mongo:27017` (pinned for compose). |
| Test environment | The child server process gets a clean environment; the tests do not set process-wide MongoDB variables except through `t.Setenv`, which restores them. |
| `go vet`, `gofmt`, `gosec`, `govulncheck` | Clean (see "How this was verified"). |
| API surface | Unchanged; S02-10 stands. |

## Regression tests

New file `backend/e2e/security_startup_test.go` (package `e2e_test`, identifiers prefixed `securityStartup`). It adds only what `startup_test.go` and the bootstrap tests do not cover: they cover the defaults, the unreachable server (in process and as a binary, with redaction of a *well-formed* URI), `STORAGE`, empty `MongoURI`/`MongoDB`, and `redactMongoURI` on eight URIs.

| Test | Pins | State |
|---|---|---|
| `TestSecurityStartupMalformedURIsDoNotLeakUserinfo` | Nine malformed URIs (bad port, `@@`, `:` in the password, wrong scheme, bad escapes in user and password, bad option values) give an error without user, password or a piece of either | active |
| `TestSecurityStartupRedactionSurvivesUnescapedSpecialCharacters` | `#`/`?` after a numeric password, `#` after the user, a missing scheme: no user, no password in the error | **skipped: SEC-S03-1** |
| `TestSecurityStartupDriverParseErrorsDoNotEchoQueryValues` | A bad escape in `tlsCertificateKeyFilePassword`, a bad `maxPoolSize`: the value is not in the error | **skipped: SEC-S03-2** |
| `TestSecurityStartupMongoTestHostErrorsNeverQuoteTheURI` | `secMongoHost` errors never contain the user, the password or `mongodb://` | active (SEC-S03-3, fixed) |
| `TestSecurityStartupServerOutputNeverCarriesTheMongoURI` | The real server on a working MongoDB (test database, marker in the URI query): start, request and SIGINT shutdown print no marker, no `mongodb://` | active |
| `TestSecurityStartupNoFallbackStorageIsLeft` | No storage field in `bootstrap.Config`, no `adapter/memory` directory or import, no `STORAGE` in compose, Dockerfile, `.dockerignore`, Makefile | active |
| `TestSecurityStartupCheckScriptRefusesWithoutMongoTestURI` | `check.sh` with `MONGO_TEST_URI` unset or empty exits non-zero, names `MONGO_TEST_URI` and `make mongo`, runs no check | active |
| `TestSecurityStartupTestsOnlyTouchRandomTestDatabases` | `Drop` only in the four reviewed helpers, every `Database("…")` literal starts with `test_`, names match `test_<16 hex>` and are unique | active |

`security_books_test.go` changed in one place: `secMongoHost` replaces the inline `url.Parse` and its message (SEC-S03-3).

## Follow-up tasks

Proposed in `.orch-proposals.json` (not committed; a human approves them):

1. **backend / sonnet, severity low (parked until a human approves)**: "Keep credentials out of MongoDB start-up errors" (SEC-S03-1 and SEC-S03-2). Rewrite `redactMongoURI` to follow the driver's parsing, return a fixed message for URI parse errors in `mongodb.Connect`, un-skip the two tests above. Scope: `backend/internal/bootstrap/**`, `backend/internal/platform/mongodb/**`, `backend/e2e/security_startup_test.go`. It names the existing test that must keep passing (`TestRedactMongoURI` rows `bad port` and `control character`) and the ones that need the server address in the error (`startup_test.go`).

Not proposed: MongoDB authentication and TLS (out of scope; accepted risk above); a janitor for leftover `test_*` databases and a guard against shared servers (an operator decision; the report states the risk); changing the default `MONGO_URI` (the story chose it); the stale `todo/adapter/memory` rules in `archtest` (the file must not be modified).

## Manual follow-ups

None. No fix needs docker or network access.
