# S-02: CRUD on books, with demo data

## Story

As an API client, I want to create, read, update, delete and list books through `/api/books`, so that the book catalog from S-01 becomes usable. As a developer, I want a fresh environment to come up with a handful of demo books, so that I can try the API right away.

## Domain

- **Bounded context:** `catalog` (exists since S-01, which only added its MongoDB schema `catalog/0001`). This story adds its domain, use cases, repositories and HTTP adapter.
- **Terms:**
  - *Book*: the aggregate. It has an *ID*, an *ISBN*, a *title*, one or more *authors*, and optionally a *published year*, a *language* and a *page count*. It tracks `createdAt`, `updatedAt` and a *version*.
  - *ISBN*: a value object holding an ISBN-13. It is a book's natural key.
  - *Catalog*: the set of all books. A client *adds* a book to it, *revises* a book, and *removes* a book from it.
  - *Demo catalog*: a fixed list of real books that can be *seeded* into an environment.
- **Rules:**
  - The field rules are the ones of the S-01 `books` schema: ISBN-13 starting with `978` or `979`; title 1–300 characters; 1–20 authors of 1–200 characters each; published year 1450–2100; language as two lowercase letters (ISO 639-1); pages 1–10000. The domain enforces them itself, so a request is rejected before it reaches the database, and the memory storage behaves like MongoDB.
  - The ISBN check digit must be valid. Hyphens and spaces in the input are removed, so `978-0-13-235088-4` is stored as `9780132350884`. Anything else that isn't a digit makes the ISBN invalid.
  - Title and author names are trimmed. A value that is empty after trimming is invalid.
  - ISBNs are unique within the catalog.
  - The ID is assigned by the repository when the book is added and never changes. With MongoDB it comes from the `counters` document `{_id: "books", seq}`. IDs are never reused, even after a delete.
  - A new book has version 1, and `createdAt` = `updatedAt`. Each revision increments the version by 1 and sets `updatedAt`. `createdAt` never changes.
  - Revising a book replaces all its editable fields (ISBN, title, authors, year, language, pages). The client must send the version it last read; if the stored version differs, the revision is rejected (optimistic concurrency).
  - Removing a book deletes it for good (no soft delete).
  - Seeding is idempotent: a demo book whose ISBN is already in the catalog is skipped, so restarting doesn't create duplicates, and a demo book a user edited or deleted is not restored.

## API

All under the `catalog` tag. Errors are `{"error": "<message>"}`, as usual.

| Operation | Route | Success | Errors |
|---|---|---|---|
| `createBook` | `POST /api/books` | 201, the book | 400 invalid input, 409 ISBN already exists |
| `listBooks` | `GET /api/books?author=&limit=&offset=` | 200, `{items, total}` | 400 invalid query |
| `getBook` | `GET /api/books/{id}` | 200, the book | 404 |
| `updateBook` | `PUT /api/books/{id}` | 200, the revised book | 400, 404, 409 ISBN already exists or version mismatch |
| `deleteBook` | `DELETE /api/books/{id}` | 204 | 404 |

- A **book** in a response: `id`, `isbn`, `title`, `authors`, `publishedYear`, `language`, `pages` (the last three omitted when not set), `createdAt`, `updatedAt` (RFC 3339), `version`.
- The **create** body has the editable fields. The **update** body has the editable fields plus `version`.
- **List:** sorted by ID ascending. `limit` 1–100, default 20; `offset` ≥ 0, default 0. `author` is optional and matches books with an author exactly equal to it (this uses the `authors` index from S-01). `total` is the number of matching books, ignoring `limit` and `offset`.

## Demo data

- When `SEED_DEMO_DATA=true`, the server adds the demo catalog at startup, through the catalog's use cases (so every demo book passes the domain rules). With `STORAGE=mongo` it runs after the migrations. It works with both storages. The default is off, and tests don't seed unless they ask for it.
- Each seeded book is logged at debug level. A summary (`seeded N demo books, skipped M`) is logged at info level.
- The demo catalog (all ISBNs have valid check digits):

  | ISBN | Title | Authors | Year | Lang | Pages |
  |---|---|---|---|---|---|
  | 9780321125217 | Domain-Driven Design | Eric Evans | 2003 | en | 560 |
  | 9780321834577 | Implementing Domain-Driven Design | Vaughn Vernon | 2013 | en | 656 |
  | 9780321127426 | Patterns of Enterprise Application Architecture | Martin Fowler | 2002 | en | 560 |
  | 9780134757599 | Refactoring | Martin Fowler | 2018 | en | 448 |
  | 9780132350884 | Clean Code | Robert C. Martin | 2008 | en | 464 |
  | 9780135957059 | The Pragmatic Programmer | David Thomas, Andrew Hunt | 2019 | en | 352 |
  | 9781449373320 | Designing Data-Intensive Applications | Martin Kleppmann | 2017 | en | 616 |
  | 9780134190440 | The Go Programming Language | Alan A. A. Donovan, Brian W. Kernighan | 2015 | en | 380 |
  | 9781942788331 | Accelerate | Nicole Forsgren, Jez Humble, Gene Kim | 2018 | en | 288 |
  | 9780262510875 | Structure and Interpretation of Computer Programs | Harold Abelson, Gerald Jay Sussman | 1996 | en | 657 |
  | 9780465026562 | Gödel, Escher, Bach | Douglas R. Hofstadter | 1999 | en | — |
  | 9782070612758 | Le Petit Prince | Antoine de Saint-Exupéry | — | fr | — |

  The last two leave optional fields empty on purpose, so the demo shows both shapes.

## Acceptance criteria

**Create**

- [ ] Given an empty catalog, when I create a complete, valid book, then I get 201 with `id`, `version` 1 and `createdAt` = `updatedAt`, and `GET /api/books/{id}` returns the same book.
- [ ] Given a book with only the required fields, when I create it, then I get 201 and the optional fields are absent from the response.
- [ ] Given the ISBN `978-0-13-235088-4`, when I create a book, then it is stored and returned as `9780132350884`.
- [ ] Given a book whose ISBN is already in the catalog, also in a different spelling (with hyphens), when I create it, then I get 409 and the catalog is unchanged.
- [ ] Given an ISBN with a wrong check digit, 12 or 14 digits, letters, or starting with `977`, when I create a book, then I get 400 with a message naming `isbn`.
- [ ] Given an empty or whitespace-only title, a 301-character title, no authors, 21 authors, an empty author name, year 1449 or 2101, pages 0 or 10001, or language `EN` or `eng`, when I create a book, then I get 400 with a message naming the field.

**Read and list**

- [ ] Given no book with ID 999, when I get it, then I get 404.
- [ ] Given 25 books, when I list without parameters, then I get the first 20 by ID and `total` 25; with `offset=20` I get the remaining 5.
- [ ] Given books by several authors, when I list with `author=Martin Fowler`, then I get only books that have exactly that author, and `total` counts only those.
- [ ] Given `limit=0`, `limit=101` or `offset=-1`, when I list, then I get 400.
- [ ] Given an empty catalog, when I list, then I get 200 with `items: []` and `total` 0.

**Update**

- [ ] Given a book with version 1, when I update it with version 1 and a new title, then I get 200 with the new title, version 2, an `updatedAt` later than or equal to before, and an unchanged `createdAt`.
- [ ] Given a book now at version 2, when I update it with version 1, then I get 409 and the book is unchanged.
- [ ] Given two books, when I change the first one's ISBN to the second one's, then I get 409.
- [ ] Given no book with ID 999, when I update it, then I get 404. Invalid fields give 400, as for create.

**Delete**

- [ ] Given a book, when I delete it, then I get 204, and getting it afterwards gives 404.
- [ ] Given no book with ID 999, when I delete it, then I get 404.
- [ ] Given a deleted book, when I create a new book, then it gets a new ID, never the deleted one's.

**Storage**

- [ ] The memory and MongoDB repositories pass the same contract suite (`repotest`): add, get, list with filter and paging, update with version check, delete, duplicate ISBN, unknown ID.
- [ ] With `STORAGE=mongo`, a stored book document passes the S-01 validator, and IDs come from the `counters` document.
- [ ] With MongoDB, two concurrent updates of the same book with the same version: exactly one succeeds, the other gets a version conflict.

**Demo data**

- [ ] Given an empty catalog and `SEED_DEMO_DATA=true`, when the server starts, then `GET /api/books?limit=100` returns the 12 demo books.
- [ ] Given the demo books are already seeded, when the server restarts with the flag, then there are still 12 books, and the log says `seeded 0 demo books, skipped 12`.
- [ ] Given a demo book was deleted, when the server restarts with the flag, then it is added again (it's missing by ISBN). Given a demo book was edited but kept its ISBN, then it is left as it is.
- [ ] Given `SEED_DEMO_DATA` is unset, when the server starts, then the catalog is empty.

## Notes for planning

- Follow "How a feature flows" in CLAUDE.md. Suggested tasks and order:
  1. **Spec:** the five operations in `openapi.yaml`, then `go generate`. Parallel with 2.
  2. **Domain + app**, test-first: `ISBN` value object (normalization, check digit), `Book` aggregate, domain errors (`ErrNotFound`, `ErrDuplicateISBN`, `ErrVersionConflict`, validation errors that name the field), `Repository` interface, `UseCases` + `Service` (with an injectable clock), and the demo catalog with a `SeedDemo` use case.
  3. **Memory repository** + the `repotest` contract suite. Depends on 2.
  4. **MongoDB repository**, run against `repotest` and the S-01 schema (`catalog/0001`). The counter is incremented atomically with `findOneAndUpdate` + `$inc` + upsert; updates filter on `{_id, version}`; the unique index error (E11000) maps to `ErrDuplicateISBN`. Depends on 3.
  5. **HTTP adapter** `catalog/adapter/httpapi`, embedded in `handler.Handler`. Depends on 1 and 2.
  6. **Bootstrap:** wire memory/mongo repositories, read `SEED_DEMO_DATA` in `ConfigFromEnv`, seed after migrations. Depends on 4 and 5.
  7. **e2e:** the acceptance criteria above, through the generated client. Depends on 6.
- No new migration is needed: S-01 already created the collection, validator and indexes. If the mongo task finds the schema has to change, that is a new `catalog/0002`; `catalog/0001` is never edited.
- The demo catalog is domain data (plain Go, in `domain` or `app`), not a migration, so it doesn't end up in production databases unless the flag is set.

## Out of scope

- Authentication and authorization (not part of this demo).
- Partial updates (`PATCH`), bulk operations, and soft delete.
- Full-text search on titles, sorting by anything other than ID, cursor-based paging.
- Authors as their own aggregate. Authors stay plain strings on the book.
- Setting `SEED_DEMO_DATA` in `docker-compose.yml` (a one-line follow-up if wanted).
- The frontend.
