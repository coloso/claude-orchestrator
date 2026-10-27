# Frontend

The web UI in `frontend/` lets a librarian browse, add, revise and remove books in the catalog. It is a client of the `catalog` API in `backend/api/openapi.yaml`.

## 1. Overview

**Stack**

- Vue 3 with TypeScript (`<script setup lang="ts">`)
- Vue Router: the screens and the list state in the URL
- Pinia: only for the flash notice (`src/stores/notice.ts`)
- openapi-fetch: the typed HTTP client, generated from the spec
- Vitest and @vue/test-utils: unit and flow tests

**Terms**

| Term | Meaning in the UI |
| --- | --- |
| *book* | A catalog entry: ISBN, title, authors, and optional published year, language and pages. |
| *ISBN* | ISBN-13. The API removes hyphens and spaces; the UI shows the normalized value the API returns, e.g. `9780132350884`. |
| *version* | A counter the API increments on every change. A revision must send the version the form was loaded with. |
| *catalog page* | The list screen at `/books`: a filter, a table and paging. |
| *book form* | The add and edit form, shared by `/books/new` and `/books/:id/edit`. |
| *version conflict* | A 409 on a revision: the book changed after the form loaded it. |

## 2. Running it

Run `npm install` once in `frontend/`. The Node version comes from `engines`: `^22.18.0 || >=24.12.0`.

Start the stack in three terminals from the repo root:

```sh
make mongo              # MongoDB on localhost:27017 (docker compose)
make backend            # API on http://localhost:8000
make frontend           # Vite on http://localhost:5173
```

- `SEED_DEMO_DATA=true make backend` seeds the demo books when the catalog is empty.
- `make frontend` runs `npm run dev` in `frontend/`. Vite proxies `/api` to `:8000`, so the browser only talks to `:5173`.

## 3. Screens and routes

| Route | Screen | API operations |
| --- | --- | --- |
| `/` | Redirects to `/books` | none |
| `/books` | Catalog (`BookListView`) | `listBooks` |
| `/books/new` | New book (`BookFormView`) | `createBook` |
| `/books/:id` | Book details, with delete (`BookDetailView`) | `getBook`, `deleteBook` |
| `/books/:id/edit` | Edit book (`BookFormView`) | `getBook`, `updateBook` |
| anything else | Page not found (`NotFoundView`) | none |

An `:id` must be a positive integer of up to 15 digits. Other values, such as `/books/abc` or `/books/0`, show "Page not found".

**List state in the URL.** The catalog keeps its state in the query string, so reload and back/forward work:

```
/books?author=Robert%20C.%20Martin&offset=40
```

- 20 books per page (`PAGE_SIZE`), sent as `limit=20`.
- `author` is omitted when empty; `offset` is omitted when 0.
- A bad or negative `offset` counts as 0.
- Applying a filter resets to the first page. The author is trimmed, and only the first value of a repeated parameter is used.
- The pager shows `1–20 of 45` with Previous and Next.

## 4. How the UI maps to the API

**One typed client.** `src/api/` is the only code that talks to the API.

- `client.ts`: one openapi-fetch client. Its base URL is the page's own origin, so requests are relative to wherever the app is served.
- `books.ts`: `listBooks`, `getBook`, `createBook`, `updateBook`, `deleteBook`, and `PAGE_SIZE`.
- `errors.ts`: `ApiError`, `ApiResult`, `toApiError`, `parseFieldError`.
- `index.ts`: the public exports. Views import from `@/api`.

Every call returns an `ApiResult<T>`, and never throws:

```ts
type ApiResult<T> = { ok: true; data: T } | { ok: false; error: ApiError }

interface ApiError {
  status: number                 // 0 for a network error
  message: string                // the API's `error` text, or a generic message
  kind: 'network' | 'invalid' | 'not-found' | 'duplicate-isbn' | 'version-conflict' | 'other'
}
```

`kind` is derived from the status. A 409 is a version conflict when its message starts with `version conflict`; otherwise it is a duplicate ISBN.

**Generated types.** `npm run generate` writes `src/api/schema.d.ts` from `backend/api/openapi.yaml`. The file is gitignored. `dev` and `type-check` run it first, so `make frontend`, `npm run typecheck` and `npm run build` always see the current spec.

- Use the schema types, re-exported from `@/api`: `Book`, `BookInput`, `BookUpdate`, `BookList`, `ListBooksQuery`.
- After a spec change, run `npm run typecheck` and fix what no longer compiles.
- Do not hand-write API types.
- Do not call `fetch` outside `src/api/`.

## 5. How errors are shown

| Situation | Where it is shown | What the user can do |
| --- | --- | --- |
| 400 whose message is `<field>: <reason>` | At that field, with the reason. The input is linked with `aria-describedby`. | Correct the field; the input is kept. |
| Any other 400 | Top of the form, in an alert. | Correct the input; the input is kept. |
| 409 duplicate ISBN | At the ISBN field: "A book with this ISBN already exists". | Change the ISBN. |
| 409 version conflict | Banner at the top of the form: "This book was changed by someone else." | *Load latest* replaces the form with the current book and discards the user's changes. Until then the input is kept. |
| 404 when loading | "Book not found" page with a link back to books. | none |
| 404 when saving an edit | "This book no longer exists." with a link back to books. | none |
| 404 when deleting | Treated as removed: the page goes to the catalog with the "Deleted …" notice. | none |
| Network error or 5xx when loading | Message with a *Retry* button. Applies to the catalog, book details and the edit form. | *Retry* |
| Network error or 5xx when saving | Message at the top of the form. There is no *Retry* button; the input is kept. | Submit again |

Rules:

- **The backend is the only authority on validation.** The form uses `novalidate`, and it does not duplicate the API's rules. It sends what the user typed and shows the 400.
- **Empty optional fields are left out** of the request body. A whitespace-only value counts as empty. Optional numbers are sent as numbers.
- **A revision sends the version the form was loaded with.** It is sent unchanged on every submit, including after a *Load latest*.
- **A version conflict is never retried automatically.** The user chooses *Load latest*.
- **Delete asks for confirmation in the page.** Cancel is focused when the question opens. Confirming deletes the book and goes to `/books` with the notice `Deleted <title>`. The notice is cleared by the next navigation or by *Dismiss*.

## 6. Tests

The frontend gate is:

```sh
cd frontend
npm run typecheck && npm test
```

- **API mock.** `src/__tests__/helpers.ts` replaces `fetch` per test with `stubFetch(handler)`, which records each request (method, URL, JSON body) and answers with the handler's response. An unhandled request gets a 501 that names the request. `setup.ts` makes every unstubbed request reject, so no test reaches the network.
- **Helpers.** `renderRoute(path)` mounts `App` with a memory router and Pinia. `makeBook()` builds a `Book`. `jsonResponse`, `errorResponse` and `noContent` build responses.
- **Flow tests.** The `views/__tests__/` specs cover the screens end to end, with the API mocked: adding, editing, the version conflict, filtering and paging, and delete.
- **Accessibility checks.** These sit in the view specs: every input has a label, required fields are marked, errors are linked with `aria-describedby`, the delete question focuses Cancel, and status and alert regions are announced.
- **Guard test.** `src/__tests__/quality.spec.ts` fails if any source file has an `any` type or uses `v-html`.
- **Where tests live.** Each folder has a `__tests__/` directory: `src/__tests__/` for routing and the notice, `src/api/__tests__/` for the client layer, and `src/views/__tests__/` for the screens.

The quality spec checks only `any` and `v-html`. The rules on `fetch` and hand-written API types are conventions, checked in review.

## 7. Source tree

```
frontend/src/
├── main.ts                 entry: Pinia, router, mount
├── App.vue                 header, notice bar, router view
├── router/index.ts         routes; createAppRouter() for tests
├── api/
│   ├── client.ts           openapi-fetch client on the page origin
│   ├── books.ts            the five catalog calls, PAGE_SIZE
│   ├── errors.ts           ApiError, toApiError, parseFieldError
│   ├── index.ts            public exports
│   └── schema.d.ts         generated, gitignored
├── components/book-form/
│   ├── FormField.vue       labelled text or number input with error
│   └── AuthorsFieldset.vue 1–20 author inputs
├── stores/notice.ts        flash notice (Pinia)
├── views/
│   ├── BookListView.vue
│   ├── BookDetailView.vue
│   ├── BookFormView.vue    add and edit
│   └── NotFoundView.vue
└── __tests__/              setup, helpers, App and quality specs
```

## 8. Not done yet

- Serving the built app in production and in `docker-compose.yml`. This is a follow-up story.
- Browser end-to-end tests.
- Internationalization. All UI text is English and inline.
