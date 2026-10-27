# S04-08: Security review of the books frontend (S-04)

Reviewed with the three screens, the API client and the Vite setup in place. Date: 2026-10-27. Reviewer role: security.

**Result: no critical, high or medium finding.** Book data, API messages and the notice are rendered as text on every screen, every link is built from a fixed path or a numeric id, nothing the user or the server types reaches a URL except the author filter (by design), and a malformed response gives an error state in every case but one. Two low findings (SEC-S04-2 and SEC-S04-1) are pinned by skipped tests and have follow-up proposals. Two low items cannot be done by an agent (dependency advisories and the headers of a future production server) and are listed under "Manual follow-ups".

## Scope reviewed

- `frontend/src/`: `main.ts`, `App.vue`, `router/index.ts`, `stores/notice.ts`, `views/` (`BookListView`, `BookDetailView`, `BookFormView`, `NotFoundView`), `components/book-form/` (`FormField`, `AuthorsFieldset`), `api/` (`client.ts`, `books.ts`, `errors.ts`, `index.ts`).
- `frontend/vite.config.ts`, `vitest.config.ts`, `index.html`, `public/`, `package.json`, `package-lock.json`, `.gitignore`, the root `Makefile` target `frontend`.
- The backend only where the UI depends on it: `backend/api/openapi.yaml` (limits of `author`, `offset`), `internal/server/server.go` (headers, error texts), `internal/catalog/domain` (error texts, `isPlainText`), and the earlier reports [S02-02](S02-02-books-api-spec.md), [S02-02.6](S02-02.6-books-implementation.md) and [S02-10](S02-10-books-api-hardening.md), read first and not repeated.
- Not reviewed: the existing frontend tests beyond reading them for conflicts, `orchestration/`, `backlog/`, how the app will be served in production (does not exist yet; see "Manual follow-ups").

### How this was verified

- **Code reading** of everything above, in the state of branch `agent/S04-08` (HEAD `9349b6f`).
- **Searches** over `frontend/` (excluding the lockfile) for `v-html`, `innerHTML`, `eval`, `new Function`, `:href`/`:src`/`:style`, `javascript:`, `target=`, `http(s)://`, `import.meta.env`, `localStorage`, `document.`, `window.`. The only hits are the router's `BASE_URL`, the dev URL in `README.md`, and the proxy target in `vite.config.ts`.
- **Regression tests** (see "Regression tests"): 3 files, run in `cd frontend && npm run typecheck && npm test` (green). The two tests that pin missing behavior were run un-skipped to confirm they fail today, then re-skipped.
- **Lockfile checks** by grep and by the new tests, offline: `lockfileVersion` 3; 458 packages, 458 `resolved` URLs (all `https://registry.npmjs.org/`) and 458 `integrity` hashes (all `sha512-`); no `link`, git, http or file entry; one `hasInstallScript` (`fsevents`, optional, macOS only).
- **Installed code read** where a claim depends on it: `node_modules/vite-plugin-vue-devtools/dist/vite.js` sets `apply: "serve"`.
- **Not verified:** `npm audit` and `npm audit signatures` (no network); `vite build` and `vite preview` were **not run**, so the contents of `dist/` are unchecked (SEC-S04-3, manual follow-ups 1 to 3). No browser was used: DOM behavior was checked in jsdom with Vue Test Utils.

## Accepted risks

| Risk | Why accepted |
|---|---|
| **No authentication or authorization** (stated once here) | The demo has no users. Anyone who can reach the app or port 8000 can add, change and delete any book, and a script injected into the page would have exactly that power too. That is why the XSS checks below matter even without auth: the catalog is writable by anyone, so every stored value is untrusted. |
| The author filter is in the URL | `/books?author=...` is the product's shareable, bookmarkable filter. It ends up in browser history, bookmarks and the address bar. It is not logged by the backend (the server writes no access log; the only log line that could carry a parameter is `slog.Debug "transport error"`) and Vite prints no per-request line by default. It cannot leak through `Referer` to another site because the app has no external link and loads no external resource; the production `Referrer-Policy` is in "Manual follow-ups". |
| Plain HTTP, no CSP in `index.html` | Local dev. Vite's dev server needs inline code and a WebSocket, so a CSP only makes sense for the production build (see "Manual follow-ups", item 4). |
| A dev server started with `--host` | `npm run dev -- --host` (or `server.host`) would publish the Vite server, the Vue devtools helper (open-in-editor endpoint) and the proxy to the network, in front of an API without authentication. The default and `make frontend` bind to loopback, and a test pins that `vite.config.ts` does not change that. |
| A web page in the developer's own browser can reach `http://localhost:8000` by DNS rebinding | The Go server does not check the `Host` header and listens on all interfaces outside Docker (SEC-S02-17). Vite protects its own server with its default `allowedHosts`. This is the "no authentication" risk reached from a browser tab; a `Host` check is a backend decision, not part of this story. |
| Hard delete, last write wins after "Load latest" | Product decisions of earlier stories. The confirmation and the version check are what the UI can do. |

## Threat model

**Assets**: the integrity of what the page shows and does (script execution in the app's origin would act with full API rights, because there is no auth); the integrity of the catalog through the UI (a delete needs the in-page confirmation, an edit carries `version`); the privacy of what the user types into the filter; availability of the page; the supply chain of the browser bundle (`vue`, `vue-router`, `pinia`, `openapi-fetch`) and of the dev tooling.

**Entry points**:

- *Stored data from the API*: `title`, `authors`, `language`, `isbn`, `publishedYear`, `pages`, dates. Written by anyone who can reach the API, so untrusted.
- *API error messages*: the `error` string of any non-2xx response, including the 400 reason `<field>: <reason>`, shown at fields and at the top of forms, in alerts, in the delete confirmation.
- *The address*: the path (`/books/:id`, `/books/:id/edit`, anything else) and the query (`author`, `offset`; every other parameter is ignored).
- *The form inputs* and the "Deleted <title>" notice (derived from stored data).
- *The dev server*: Vite on `localhost:5173`, the `/api` proxy to `localhost:8000`, the Vue devtools plugin.
- *The build and install path*: `package.json`, `package-lock.json`, the npm registry.

**Trust boundaries**: other web page or link → browser address bar → router (id pattern, `first()`, `parseOffset`) → API client (openapi-fetch, same origin) → Vite proxy → Go backend → MongoDB; response → `call()` (`toApiError`, no body shape check) → components → Vue's text interpolation → DOM; npm registry → `node_modules` → bundle.

| STRIDE | Threat | Result |
|---|---|---|
| **S**poofing | Out of scope (no auth). A message that looks like an app message: server text placed at a field or at the top of a form; a title that imitates the "Deleted" notice or the confirmation. | Accepted for auth. Placement: only the six form-field names place a 400 reason at a field (`__proto__`, `constructor`, case and space variants go to the top); a 409 never shows its text. Look-alike text stays text. Bidi override: SEC-S04-2. |
| **T**ampering | Stored XSS and DOM injection through any stored value or server message; `javascript:` or other URLs in `href`/`src`; open redirect through the query; request smuggling through the author value (`&offset=…`, `#`, `/../`); forged ids; a page opened with a hostile URL changing data (CSRF-like). | Nothing found; all pinned (see below). The UI sends no write on load, and every write needs a click or a form submit. A cross-site page cannot write through the API (JSON content type, no CORS headers, see the S02 reports). |
| **R**epudiation | No audit trail of who changed what. | Accepted with the auth risk. |
| **I**nformation disclosure | Internal text from the server shown to the user; the filter in `Referer`, logs or history; secrets in the bundle; source maps; storage of data in the browser. | Server messages are the fixed texts reviewed in S02-10. The filter is in history only (accepted above). No `import.meta.env` variable, no `.env`, no storage API, no source maps configured. |
| **D**enial of service | A huge `author`, an offset beyond range, repeated parameters, a slow or broken backend, a huge response, a 429 from the rate limiter. | The page shows an error and a retry for each; no loop, no request storm (one request per navigation, the `latest` counter drops stale answers). A malformed shape is SEC-S04-1. |
| **E**levation of privilege | Dependencies running install scripts; a devtools plugin in production; a dev server published to the network. | One optional, platform-specific install script (`fsevents`); devtools apply to `serve` only; loopback default (pinned). |

## Findings

| ID | Title | Severity | Location | Impact | Recommendation | Follow-up |
|---|---|---|---|---|---|---|
| SEC-S04-1 | API responses are cast, not checked | low | `src/api/books.ts:25` (`data as T`), `src/views/BookListView.vue:126` (`book.authors.join`) | A 200 with valid JSON that is not a `Book` or `BookList` is accepted as one. Example (run in jsdom): `{"items":[{"id":1,"title":"x"}],"total":1}` makes the list view throw in its render function. Vue logs "Unhandled error during execution of render function" and the page is left half-drawn, with no error message and no Retry. Reachable only if something other than this backend answers on the proxied port, or the backend breaks its own contract; no user input or stored book can trigger it (the backend always sends `authors`). | Check the body against the schema before returning `ok: true` (small type guards for `Book` and `BookList`, no new dependency). A wrong shape becomes the existing error state with a fixed text. | Proposal 1; `SECURITY SEC-S04-1` in `security_client.spec.ts` (skipped) |
| SEC-S04-2 | Bidirectional control characters in titles reorder the text around them | low | `src/views/BookDetailView.vue:114` (`Delete "{{ book.title }}"? …`), `src/App.vue:22` (`{{ notice.message }}`) | The backend rejects control characters (category Cc, `domain/book.go:135`) but accepts format characters (Cf), so a title may contain U+202E (right-to-left override) or U+2066 to U+2069. In the delete confirmation the override reorders the rest of the sentence (`"? This cannot be undone.`) into a misleading or unreadable line. In the notice the title is the last thing in the line, so today the override has nothing to reorder; wrapping it is defense in depth for the day text is added after it. The text stays text (no markup), and the buttons still say what they do, so this is a display spoof, not an action spoof. | Wrap user-supplied text that sits inside a sentence in `<bdi>` (confirmation title; the notice message). Also reject the override and isolate characters (U+202A to U+202E, U+2066 to U+2069) in the backend text rule, which would protect other consumers too; not proposed here because it changes domain rules and their tests (it extends SEC-S02-15). | Proposal 2; `SECURITY SEC-S04-2` in `security_xss.spec.ts` (skipped) |
| SEC-S04-3 | Dependency advisories are not verified | low (unverified) | `frontend/package.json`, `frontend/package-lock.json` | The structure of the lockfile is sound (see "Checked and fine"), but no advisory database could be queried offline, so known vulnerabilities in the locked versions are neither found nor ruled out. Only `vue`, `vue-router`, `pinia`, `openapi-fetch` (and their dependencies) reach the browser; the rest is dev tooling, including the Vite dev server that serves files from the repository. Locked versions I could not check: `vite` 8.3.5, `vue` 3.5.44, `vue-router` 5.4.0, `pinia` 4.1.0, `openapi-fetch` 0.17.0, `openapi-typescript` 7.13.0, `vitest` 4.1.12, `jsdom` 30.1.3, `vite-plugin-vue-devtools` 8.2.1, `rolldown` 1.2.15, `postcss` 8.5.32, `undici` 8.12.3, `picomatch` 2.3.2, `typescript` 6.0.3 and all others. | Run `npm audit` and `npm audit signatures` and act on the result. | manual (items 1 and 2) |
| SEC-S04-4 | The production page will need its own security headers | low | none yet (`npm run preview` is the only way to serve `dist/` today and sends none) | When a story serves the built app, the default is a page with no CSP, no `Referrer-Policy` (the author filter in the URL would be sent as `Referer` to any later external link) and no `frame-ancestors` (clickjacking of the delete button). Not exploitable today: nothing serves the build. | Add the headers listed under "Manual follow-ups", item 4, in that story. | manual (item 4) |

No finding needs a backend or spec change. The 409 distinction by the message prefix `version conflict` (`src/api/errors.ts:45`) is a fragile contract, not a vulnerability: the text is a fixed server text that a user cannot influence (S02-10 pins it), and an unrecognized 409 gets the fixed duplicate-ISBN text, never the server text. A machine-readable error code would be cleaner, but it is a spec change with no abuse path to justify a security proposal; it is recorded here for the story that next touches the error model.

## Checked and fine

| Check | Result |
|---|---|
| XSS: markup in data | No `v-html`, `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `srcdoc`, `eval`, `new Function` or string timers in `src/`. All dynamic text uses `{{ }}`. Pinned: six payloads (`<img src=x onerror=alert(1)>`, `<script>`, `<svg onload>`, an attribute breakout with an `<iframe src=javascript:…>`, `javascript:alert(1)`, `{{ 7 * 7 }}`) in title, authors, language and ISBN create no element and no attribute on the catalog page, the empty-filter state, the details page, the delete confirmation, a delete error, the notice, form field errors, top-level form errors (400, 500), the revise form's load error and the failed "Load latest" error. The `{{ 7 * 7 }}` payload shows literally, so nothing is compiled at run time. |
| XSS: attributes and URLs | No `:href`, `:src`, `:style`, `:srcdoc`, `:action`, `v-bind="…"` in any template; the only bound attributes are `id`, `for`, `type`, `value`, `aria-*`, `to` (router) and `key`. Pinned by a source scan with a self-check of the matcher, and by a DOM check that no attribute is `on*`, `style` or `src` and none contains `javascript:` (an input's `value` is data, not a URL). |
| Runtime template compiler | None: no `template:` option, no `compile()`, no `vue/dist/vue.esm-*` alias (the only alias is `@`). Pinned. |
| Link targets | Every `RouterLink`/`href` is `/books`, `/books/new`, `/books/<id>`, `/books/<id>/edit` or `/books?author=…&offset=…`; ids come from `Number()` of a route param matched by `[1-9][0-9]{0,14}` or from the API's numeric `id`. No `target`, so no `_blank`; the source scan requires `rel="noopener noreferrer"` if one is added. The scaffold's external links, logo and docs are gone (`App.vue` has none; `public/` holds only `favicon.ico`; `index.html` has no external URL). |
| No open redirect | The router has one redirect, `/` → `books`. No `redirect`/`next`/`returnTo` query is read anywhere; with `?redirect=https://evil.example&next=//evil.example&returnTo=javascript:alert(1)` the page stays on `/books` and the API call carries only `author`, `limit`, `offset`. No `window.open`, `location.assign/replace/href =`, `postMessage` in `src/` (pinned). |
| Routes | Ids `0`, `01`, `-1`, `1e3`, 16 digits, an encoded `<img>`, a script path, `/javascript:alert(1)` and `//evil.example/books` all give the "Page not found" screen, which does not echo the path, with no API request. |
| What reaches the URL | Only `author` (trimmed, omitted when empty) and `offset` (omitted when 0) from the list page. After a delete the URL is `/books`; after a failed save it is unchanged; after a save it is `/books/<id>`. Titles, error messages and the notice (a Pinia store) never reach the URL or the history state. Pinned. |
| Query parsing | Repeated `author` and `offset`: the first wins. Offset `-5`, `abc`, `1e3`, `1.5`, `0x10`, `+5`, ` 7`, NUL, empty, or beyond the safe integers: 0. A safe but out-of-range offset (`9007199254740991`) is sent and answered with 400, shown with Retry. A 50,000-character `author` is sent once and the resulting 431 text is shown as `Request failed with status 431`, with a button. Unknown parameters and `limit` in the page URL are not forwarded; `limit` is always 20. |
| Request encoding | openapi-fetch encodes the author (`a&offset=5&limit=100#x <b>é/../?` arrives as one `author` value; no extra key, no `#`, `<`, `>` or space in the query). Path ids are numbers. Pinned. |
| Same origin | `baseUrl` is `globalThis.location.origin`; `fetch` is looked up per call; no host, `credentials` or `mode` setting in `src/`. Every request in the tests goes to `/api/books…` on the page origin, with no user info or fragment. The UI therefore needs no CORS headers on the backend (the S02 reports ask that none be added). |
| Errors are only displayed | `messageOf` keeps a body only if `error` is a string; numbers, objects, arrays, `null` and plain strings give `Request failed with status N`. An HTML error page from a proxy (502) shows that generic text and none of its markup. A rejected `fetch` gives "Could not reach the server." Messages are used for display and for two decisions: the `version conflict` prefix and the 400 `<field>: ` split. |
| The 400 field parsing | `parseFieldError` splits at the first `: ` and accepts only the six exact field names, by equality over an array (not by property lookup), so `__proto__`, `constructor`, `toString`, `Title`, ` title`, `title :` and `<b>title</b>` are not fields. The reason is text. The server's 400 reasons are fixed texts that do not echo the input (S02-10). |
| The 409 distinction | A 409 whose text starts with `version conflict` shows the fixed conflict box; any other 409 shows the fixed "A book with this ISBN already exists" at the ISBN field, never the server text (pinned with an injected suffix). |
| Malformed responses | A 200 with a non-JSON body, an HTML body, an empty body and a 204 on a GET give an error state with Retry on the list, details and revise screens, with no unhandled rejection (`call()` catches everything; Vitest fails the run on one). The exception is a JSON body of the wrong shape: SEC-S04-1. |
| Double actions | Save, delete and "Load latest" are guarded by `saving`/`deleting`/`reloading`; list responses are matched to the latest request (`latest`). No duplicate writes from a double click. |
| Vite proxy | One entry, `'/api'` → `http://localhost:8000`. Pinned: only that key, `http:`, loopback host. A string key matches by prefix, so `/apix` is also forwarded; the backend answers 404 JSON and the UI never requests such a path, so no finding. `changeOrigin` is off (the backend sees `Host: localhost:5173`; it does not use it). No `ws`, no rewrite. |
| Dev server binding | No `server.host`, `allowedHosts`, `cors` or `fs` setting: loopback only, Vite's host-header protection and `fs.strict` stay on. Pinned. `make frontend` runs `npm run dev` with no flags. |
| Devtools in production | `vite-plugin-vue-devtools` 8.2.1 sets `apply: "serve"` (read in `node_modules`, and pinned by a test that resolves the configured plugins), and the package is a `devDependency`. The built output was not inspected (manual item 3). |
| Source maps, env, secrets | No `build.sourcemap`, so none in the build (pinned). No `envPrefix`, no `define`, no `.env*` file, `import.meta.env` only as `BASE_URL`; nothing starting with `VITE_` is read. No secret anywhere in `frontend/` (the app has none to hold). Pinned. |
| Browser storage | No `localStorage`, `sessionStorage`, `indexedDB` or cookie access. State is in memory (Pinia notice store) and the URL. Pinned. |
| `index.html` | One `<script type="module" src="/src/main.ts">`, nothing inline, no inline handler, no `<base>`, `<iframe>`, `<object>`, `<embed>` or `http-equiv`. Pinned. |
| Dependency files | `lockfileVersion` 3; every one of the 458 packages from `https://registry.npmjs.org/` with a `sha512-` integrity; no git, http, file, tarball or `link` entry; every range in `package.json` a plain `^`, `~` or exact version; lockfile root entry equal to `package.json`; every direct dependency locked at or above its range's floor and inside its major (inside its minor for `~`, exactly for pins); no lifecycle script in `package.json`; no `.npmrc`; four runtime dependencies (pinned, so a fifth is a deliberate change). One `hasInstallScript`: `fsevents`, `optional`, `os: darwin`. One `overrides` entry, `openapi-typescript` → `typescript: "$typescript"`, which makes the generator use the project's TypeScript 6 instead of its own and adds no package. All pinned. |
| Gate | `cd frontend && npm run typecheck && npm test` is green; the existing guard (`quality.spec.ts`) still finds no `any` and no `v-html` in the new test files. |

## Regression tests

`frontend/src/__tests__/security_*.spec.ts`, using the helpers in `helpers.ts`. Each test pins the secure behavior. The build test runs in the `node` environment and reads files and the Vite config; the rest run in jsdom.

| File | Pins | State |
|---|---|---|
| `security_xss.spec.ts` | Six payloads × (catalog page, empty-filter state, catalog error, details page, delete confirmation, delete error, details load error, notice, form field error, top-level 400 and 500, 409 text hidden, revise form fields, revise load error, failed "Load latest"): no element outside the app's own tags, no `on*`/`src`/`style` attribute, no script URL, text shown literally. A checker self-test proves the check fails on injected markup. A title with U+202E still renders as text. | active |
| `security_xss.spec.ts` | `SEC-S04-2`: the confirmation title and the notice message sit in `<bdi>` | **skipped** |
| `security_client.spec.ts` | Same-origin `/api/books…` requests without user info or fragment; encoding of the author; only `author`, `limit`, `offset` leave; numeric ids in the path; ten offset spellings → 0; first of repeated parameters; huge author and out-of-range offset give an error with a button; redirect-style parameters do nothing; URL after filter, page, delete, failed save and save; links on five screens and the not-found screen for nine hostile addresses with no API call; non-JSON, HTML, empty and 204 bodies; HTML proxy error; five unusable `error` shapes; network failure; `parseFieldError` on twelve non-fields and the six fields; fixed 409 text | active |
| `security_client.spec.ts` | `SEC-S04-1`: a list item without `authors` gives the error state, not a half-rendered page | **skipped** |
| `security_build.spec.ts` | Source scan for 12 classes of dangerous construct plus `target` without `rel` (with a 23-case positive and 10-case negative self-check of the matcher); `index.html`; `public/` and `.env*`; Vue devtools `apply: 'serve'`; no source maps; loopback host, no `allowedHosts`/`cors`/`fs` override; one `/api` proxy to a local `http:` target; no `envPrefix`/`define`; one alias; lockfile version, sources, integrity, install scripts, ranges, overrides, runtime dependency list, lockfile in sync with `package.json` | active |

Existing tests that assert the current behavior of anything the proposals change: `BookDetailView.spec.ts` reads `.notice` text (`Deleted Clean Code`) and `App.spec.ts` the notice store; `<bdi>` does not change `.text()`, so no change is expected (stated in proposal 2). Existing list fixtures send `{ items, total }` only; the guard of proposal 1 accepts that shape.

## Follow-up tasks

Proposed in `.orch-proposals.json` (not committed; a human approves them). Both are `low`, so the orchestrator parks them until a human approves.

1. **frontend / sonnet** (low): check the shape of API responses before using them (SEC-S04-1). Scope `frontend/src/api/**` and `security_client.spec.ts`. Un-skips `SECURITY SEC-S04-1`.
2. **frontend / haiku** (low): isolate book text from bidirectional controls (SEC-S04-2). Scope `BookDetailView.vue`, `App.vue`, `security_xss.spec.ts`. Un-skips `SECURITY SEC-S04-2`.

Not proposed: a machine-readable error code for the 409 distinction (see the note under "Findings"); rejecting bidi controls in the backend (recommended in SEC-S04-2, extends SEC-S02-15); anything about authentication.

## Manual follow-ups

None of these can be done by an agent (no network, no `npm install`, no build or preview server in the worker tools).

1. **Advisories (SEC-S04-3).** In `frontend/`:
   ```
   npm ci
   npm audit
   npm audit --omit=dev
   ```
   Read the second result first: only those packages reach the browser. For anything that has a fix in range, `npm update <package>` and commit the lockfile; for anything else, decide by reading the advisory. Give particular attention to `vite` (the dev server serves repository files) and `vue-router`/`vue`.
2. **Provenance.** `npm audit signatures` after `npm ci`. It fails on a package whose registry signature or provenance does not verify.
3. **Check the build output once.** `npm run build-only`, then:
   ```
   ls dist dist/assets
   grep -rl "vue-devtools\|__VUE_DEVTOOLS" dist
   grep -c "<script" dist/index.html
   ```
   Expect: no `.map` file, no devtools string, and only the module `<script type="module" src="/assets/…">` in `index.html` (no inline script). If a modulepreload polyfill or other inline script shows up, the CSP below needs a hash or `build.modulePreload.polyfill: false`.
4. **When a story serves the built app** (a static server, or the Go server), set on the HTML and the assets:
   - `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`. The app uses no inline script, no inline style attribute, no `eval` (no runtime compiler) and calls only its own origin, so this should work unchanged; check the console after the first load. If the app is served from another origin than the API, `connect-src` must name that origin, and the API then needs an explicit CORS origin (never `*`).
   - `Referrer-Policy: no-referrer` (or `same-origin`), because the author filter is in the URL.
   - `X-Content-Type-Options: nosniff`, and `Cache-Control: no-cache` on `index.html` (hashed assets may be immutable).
   - `frame-ancestors 'none'` is in the CSP above; add `X-Frame-Options: DENY` for old browsers.
   - SPA fallback: unknown paths outside `/api/` return `index.html` with 200, but `/api/…` must never fall back to HTML (the client would treat an HTML 200 as an error; the tests pin that it does, but the behavior should not depend on it).
   - Disable directory listings, and serve `dist/` only (never the project root).
5. **Dev hygiene.** Do not run the dev server with `--host`; if a remote demo is needed, put authentication in front of both the UI and the API first.
