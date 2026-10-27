# S-04: Books frontend (Vue 3 + TypeScript)

## Story

As a librarian, I want a web UI for the book catalog, so that I can browse, add, revise and remove books without crafting API calls by hand. It uses the existing `/api/books` endpoints from S-02. The backend does not change.

## Domain

- **Bounded context:** none new. The UI is a client of `catalog` and uses its language: a *book* is *added* to the *catalog*, *revised* and *removed*.
- **Terms:**
  - *Book*, *ISBN*, *version*: as in S-02. The UI shows the ISBN as the API returns it, normalized (`9780132350884`).
  - *Catalog page*: the list of books, 20 per page, optionally filtered by an exact author name.
  - *Book form*: the shared form for adding and revising a book.
  - *Version conflict*: the book was revised by someone else since the form was loaded (API 409 on update).
- **Rules:**
  - The backend is the only authority on validation. The form may mark required fields and use number inputs, but it does not copy the S-02 field rules. Every 400 from the API must be shown, wherever it comes from.
  - A 400 message has the form `<field>: <reason>`, e.g. `title: must have 1 to 300 characters`. If `<field>` is one of the form's fields (`isbn`, `title`, `authors`, `publishedYear`, `language`, `pages`), the reason is shown at that field. Any other message is shown at the top of the form.
  - Optional fields left empty are **omitted** from the request body. They are not sent as `""`, `0` or `null`.
  - A revision always sends the `version` the form was loaded with. The UI never retries with a newer version on its own, because that would overwrite someone else's change.
  - The list state (page and author filter) lives in the URL query (`/books?author=…&offset=…`), so reload, back and shared links keep it.

## API access

- Types come from the spec: `npm run generate` (it also runs before `dev` and `type-check`) writes `src/api/schema.d.ts` from `backend/api/openapi.yaml` with openapi-typescript. The file is gitignored and must never be edited or replaced by hand-written types.
- All calls go through one typed client in `src/api/`, built with `openapi-fetch` on top of the generated `paths`. Components and views never call `fetch` directly.
- The client returns either the data or a typed error `{status, message}`. The two different 409s on update are told apart in one place in `src/api/`: the message starts with `version conflict` for a version conflict, and anything else is a duplicate ISBN.
- URLs are relative (`/api/books`). In development, Vite proxies `/api` to the backend on :8000 (`make backend`, then `make frontend`).

## Screens and routes

| Route | Screen |
|---|---|
| `/` | redirects to `/books` |
| `/books` | catalog page: a table with title, authors, published year and language. An author filter (exact match). Paging with *Previous* / *Next* and "21–25 of 25". A *New book* button |
| `/books/new` | book form, empty |
| `/books/:id` | book details: all fields, `createdAt`/`updatedAt` in the browser's locale, the version. *Edit* and *Delete* |
| `/books/:id/edit` | book form, filled from `getBook` |
| anything else | "Page not found" with a link to `/books` |

- Authors are edited as a list of text inputs, with *Add author* and *Remove*. There is at least one input, and *Add author* is disabled at 20.
- Delete asks for confirmation in the page itself (not `window.confirm`), naming the book's title.
- While a request runs, its button is disabled, so a double click doesn't send twice.
- Loading, empty and error states are visible. No screen stays blank or throws an unhandled error.

## Acceptance criteria

Each criterion is covered by at least one Vitest + @vue/test-utils test, with the API mocked at the `fetch` boundary.

**Catalog page**

- [ ] Given 25 books, when I open `/books`, then I see the first 20 by ID and "1–20 of 25". *Next* shows the remaining 5 with "21–25 of 25", and the URL has `offset=20`.
- [ ] Given I'm on the first page, *Previous* is disabled. Given I'm on the last page, *Next* is disabled.
- [ ] When I filter by `Martin Fowler`, then the list is requested with `author=Martin Fowler` and `offset` 0, the URL has the filter, and clearing the filter shows all books again.
- [ ] When I open `/books?author=Martin%20Fowler&offset=20` directly, then that filter and page are loaded.
- [ ] Given an empty catalog, when I open `/books`, then I see "No books yet" and the *New book* button.
- [ ] Given the API fails (network error or 500), when I open `/books`, then I see an error message and a *Retry* button that reloads the list.

**Details**

- [ ] Given book 7 exists, when I open `/books/7`, then I see all its fields, and optional fields that aren't set are shown as "—".
- [ ] Given no book 999, when I open `/books/999`, then I see "Book not found" with a link to `/books`.

**Add**

- [ ] When I submit the form with ISBN `978-0-13-235088-4`, title, two authors and no optional fields, then `createBook` gets `{isbn, title, authors}` without `publishedYear`, `language` or `pages`, and I land on the new book's details page, which shows the ISBN as `9780132350884`.
- [ ] Given the API answers 400 `title: must have 1 to 300 characters`, then that reason is shown at the title field, and what I typed is kept.
- [ ] Given the API answers 400 with a message that names no form field, then it is shown at the top of the form.
- [ ] Given the API answers 409 (duplicate ISBN), then "A book with this ISBN already exists" is shown at the ISBN field.
- [ ] *Add author* adds an input, *Remove* removes one. The last input can't be removed, and *Add author* is disabled at 20 authors.

**Revise**

- [ ] Given book 7 at version 3, when I open `/books/7/edit`, change the title and save, then `updateBook` gets all editable fields plus `version: 3`, and I land on the details page showing version 4.
- [ ] Given the API answers 409 version conflict, then I see "This book was changed by someone else", my input is kept, and a *Load latest* action reloads the form with the current book and version (discarding my changes, after I click it).
- [ ] Given the API answers 409 duplicate ISBN, then it is shown at the ISBN field, as when adding.
- [ ] Given no book 999, when I open `/books/999/edit`, then I see "Book not found".

**Remove**

- [ ] Given book 7, when I click *Delete*, then a confirmation naming its title appears. *Cancel* closes it without calling the API. *Delete* calls `deleteBook` and returns to `/books` with a "Deleted <title>" notice.
- [ ] Given the book was already removed (404 on delete), then I also return to `/books`. The book is gone either way.

**Quality**

- [ ] `npm run typecheck && npm test` passes (the frontend gate). No `any` in `src/`, and no hand-written types for API shapes.
- [ ] No `v-html` anywhere. Book data is user input and is only rendered as text.
- [ ] Every input has a `<label>`, and a field's error is linked to it with `aria-describedby`.

## Notes for planning

- The scaffold is ready and committed on the base branch: Vue 3.5, TypeScript 6, Vue Router, Pinia, Vitest, `openapi-fetch`, `openapi-typescript`, and the `/api` proxy in `vite.config.ts`. Agents can't run `npm install`, so **no task may add dependencies**. If one seems needed, propose it.
- Every task's scope is under `frontend/**`, except the docs and security reports (`docs/**`, plus `frontend/README.md` for docs). The frontend gate runs `npm run typecheck && npm test` in `frontend/`.
- Remove the scaffold's demo content (the `counter` store, the "You did it!" `App.vue` and its test) in the first task. Use Pinia only if a view needs shared state. Route-level state in the URL is preferred.
- Suggested tasks and order:
  1. **App shell + API client:** router with the routes above (views can be placeholders), layout with a header link to `/books`, the "Page not found" view, and `src/api/` (the client, the `{status, message}` error type, the 400-field parser, the 409 distinction), with unit tests.
  2. **Catalog page** (list, filter, paging, empty and error states). Depends on 1.
  3. **Book details + delete** (with confirmation and notice). Depends on 1.
  4. **Book form** for add and revise (authors list, omitted optionals, 400 at fields, both 409s, *Load latest*). Depends on 1.
  5. **Docs:** `docs/frontend.md` (screens, how the UI maps to the API, how errors are shown, `make backend` + `make frontend`, `npm run generate` after spec changes) and a rewrite of the scaffold's `frontend/README.md` pointing to it. Depends on 2–4.
  6. **Security review** of the frontend (XSS, link targets, what reaches the URL, and the pinned dependencies in `package-lock.json`; agents have no network, so no `npm audit`). Report in `docs/security/`. Fixes come back as proposals with `role: "frontend"`. Depends on 2–4.
- Tasks 2, 3 and 4 can run in parallel. To avoid merge conflicts, each owns its own view files. Only task 1 touches `router/index.ts`, and later tasks import the views task 1 registered as placeholders, with the agreed file names: `views/BookListView.vue`, `views/BookDetailView.vue`, `views/BookFormView.vue`, `views/NotFoundView.vue`.

## Out of scope

- Authentication and authorization (not part of this demo).
- Backend or spec changes. If the UI needs one, e.g. a machine-readable error code instead of matching `version conflict`, it becomes a proposal.
- Serving the built app in production or in `docker-compose.yml` (no `frontend` service yet). This is a follow-up story.
- Browser end-to-end tests (Playwright or Cypress). Component tests with a mocked API cover the criteria.
- A UI component library, i18n (the UI is English), dark mode, sorting other than by ID, free-text search.
