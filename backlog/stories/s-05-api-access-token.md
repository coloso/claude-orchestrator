# S-05: API access token between frontend proxy and backend

## Story

As the catalog's operator, I want the backend to answer only requests that come through our frontend, so that nobody can bypass the frontend and call the API directly.

The frontend's server-side proxy adds a shared secret, the *API token*, to every `/api` request. The backend rejects every request without it. The token never reaches the browser: it isn't in the JS bundle, in any response or in any browser storage.

Deliberately simple: one static shared secret, no login, no users, no JWT.

## Domain

- **Bounded context:** none. This is transport security and lives in `internal/middleware` and `internal/server`, like the rate limit. No `domain` or `app` code changes, and `catalog` knows nothing about it.
- **Terms:**
  - *API token*: the shared secret, sent as `Authorization: Bearer <API token>`.
  - *Frontend proxy*: the server that serves the frontend and forwards `/api` to the backend. For now that's Vite's dev proxy. A production proxy (nginx/Caddy service in `docker-compose.yml`) is a follow-up story.
  - *Protected request*: every request under `/api` except `GET /api/health`.
- **Rules:**
  - A protected request is answered only if its `Authorization` header is exactly `Bearer <API token>`. Otherwise the answer is 401 `{"error": "unauthorized"}` with `WWW-Authenticate: Bearer`. The response never says why (missing, malformed or wrong).
  - The comparison is constant-time and doesn't leak the token's length. For example, compare SHA-256 digests with `crypto/subtle`.
  - Unknown `/api` routes also get 401, not 404, so unauthenticated clients learn nothing about which routes exist.
  - `GET /api/health` stays public. Docker and the `-healthcheck` flag use it.
  - The check runs after the rate limit (failed attempts still use up rate-limit tokens) and before the body is read. The chain becomes: security headers → rate limit → API token → body limit → ogen server.
  - The token is never logged and never appears in an error message.
  - Standard library only. **No new Go modules.**

## API and spec

- `backend/api/openapi.yaml` declares an `apiToken` security scheme (`type: http, scheme: bearer`) as the global default, with `security: []` on `getHealth`. That documents the contract. Enforcement stays in the middleware, so ogen's generated `SecurityHandler` stays unused. If declaring the scheme forces a `SecurityHandler` onto `oas.NewServer`, the task implements it as a pass-through and says so in a comment.
- Operation descriptions mention the 401.
- The frontend's generated types don't change in any way the UI code relies on. The UI never sets the header.

## Configuration

- **Backend:** `bootstrap.ConfigFromEnv` reads `API_TOKEN`. It is required and at least 32 characters (`openssl rand -hex 32` gives 64). If it is missing or too short, the server exits at startup with an error naming `API_TOKEN`, never its value.
- **Frontend dev proxy:** `vite.config.ts` reads `API_TOKEN` from `process.env` (**not** a `VITE_`-prefixed variable, because those are bundled into the browser code) and sets it as the `Authorization` header on the `/api` proxy. This replaces any `Authorization` header the browser sent. Without `API_TOKEN`, `npm run dev` fails with a message saying how to set it.
- **Makefile:** `make backend` and `make frontend` both need the same `API_TOKEN` in the environment. If it is unset, they stop with a hint (`export API_TOKEN=$(openssl rand -hex 32)`).
- **docker-compose.yml:** the backend service gets `API_TOKEN: ${API_TOKEN:?set API_TOKEN}`, so `docker compose up` refuses to start without it. The backend port stays bound to `127.0.0.1`.

## Acceptance criteria

**Backend** (e2e through the generated client unless noted)

- [ ] Given the right token, then every `/api/books` operation behaves exactly as in S-02.
- [ ] Given no `Authorization` header, a wrong token, a token with an extra or missing character, `Basic …` instead of `Bearer …`, or `Bearer` with an empty token, when I call any `/api/books` operation, then I get 401 `unauthorized` with `WWW-Authenticate: Bearer`. Each case is its own test.
- [ ] Given no token, when I `createBook`, then no book is stored, and a request with a 2 MB body is rejected with 401 without its body being read.
- [ ] Given no token, when I call an unknown route like `GET /api/nope`, then I get 401.
- [ ] `GET /api/health` answers 200 without a token.
- [ ] Given no token, repeated requests still hit the rate limit (429 after the burst).
- [ ] Given `API_TOKEN` is unset or shorter than 32 characters, then `bootstrap.ConfigFromEnv` (or `New`) returns an error naming `API_TOKEN` that doesn't contain the value (unit test).
- [ ] The middleware has unit tests in `internal/middleware`, including the public health route and the constant-time comparison helper.
- [ ] The shared e2e helper (`e2eStart`) gives every test a client that sends the token, so the existing e2e tests pass without other edits. The new cases go in `security_api_token_test.go`.
- [ ] `internal/archtest` passes unchanged, and `backend/check.sh` passes.

**Frontend**

- [ ] `vite.config.ts` sets `Authorization: Bearer <API_TOKEN>` on the `/api` proxy, read from `process.env.API_TOKEN`, and fails clearly without it.
- [ ] Nothing under `frontend/src/` mentions `API_TOKEN` or sets an `Authorization` header. A test in `src/__tests__/security_build.spec.ts` checks this, and checks that no `VITE_*TOKEN*` variable is used anywhere in the frontend.
- [ ] `npm run typecheck && npm test` passes.

**Docs**

- [ ] `docs/api-access.md` explains the flow (browser → frontend proxy adds the token → backend), why the token must never be in the browser, `API_TOKEN` for backend, Vite and compose, how to generate and rotate it (change it on both sides, then restart), and what this does and does not protect (see below). `README.md` and `CLAUDE.md` mention `API_TOKEN`, and that `make backend`/`make frontend` need it.

## What this protects, and what it doesn't

- ✅ Direct calls to the backend that skip the frontend proxy are rejected.
- ❌ Anyone who can open the frontend can still use the API through it. This is not user authentication.
- ❌ If the token leaks (logs, a committed `.env`, a `VITE_` variable), it protects nothing until it is rotated.
- ⚠️ All proxied traffic comes from the proxy's address, so the per-client rate limit now treats all users as one client. Trusting `X-Forwarded-For` from the proxy is a follow-up, and `docs/api-access.md` says so.

## Notes for planning

- **Order:**
  1. **Spec and config:** the security scheme in `openapi.yaml`, `go generate`, and `API_TOKEN` in `bootstrap.Config`/`ConfigFromEnv`.
  2. **Middleware and wiring:** the middleware + server option + bootstrap wiring, **together with** the shared e2e helper update. The existing e2e suite breaks as soon as the check is on, so this one task owns `e2e_test.go`.
  3. **Security e2e tests:** `security_api_token_test.go`. Depends on 2.
  4. **Frontend:** Vite proxy and the build/security test. Can run in parallel with 2.
  5. **Tooling and docs:** Makefile, `docker-compose.yml`, `docs/api-access.md`, `README.md`, `CLAUDE.md`. Depends on 2 and 4.
  6. **Security review:** the comparison, secrets in logs and errors, the middleware order, the Vite proxy config, the bundle. Report in `docs/security/`, fixes as proposals. Depends on 2–5.
- **Gate:** from this story on, the backend gate's e2e tests set the token themselves (via the helper), so `orchestration/` should need no change. If it does, humans make it.

## Out of scope

- User login, JWTs, sessions, roles. That's a separate story if individual users need to be told apart.
- A production frontend service or reverse proxy in `docker-compose.yml`, and removing the backend's published port. Follow-up story (already listed as out of scope in S-04).
- `X-Forwarded-For` handling for the rate limit behind the proxy.
- More than one token, token rotation without a restart, mTLS.
- CORS changes. The API still sends no `Access-Control-*` headers.
