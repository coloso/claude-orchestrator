# S-01: Database schema platform, with a books collection

## Story

As a backend developer, I want every bounded context to declare its MongoDB schema as versioned migrations that are applied automatically and safely at startup, so that the database always matches the code, invalid documents are rejected by the database itself, and nobody runs schema changes by hand.

The first schema is a simple book catalog: one `books` collection.

## Domain

### Schema platform

Shared infrastructure in `backend/internal/platform/mongodb/`, used by each context's `adapter/mongodb`.

- **Terms:**
  - *Migration*: one forward-only, numbered schema change. It has an *ID* `<context>/<NNNN>` (e.g. `catalog/0001`), a *description*, and an *Up* step.
  - *Migration set*: all migrations of one context, in sequence order.
  - *Ledger*: the `schema_migrations` collection, recording each applied migration (`_id` = migration ID, `description`, `appliedAt`, `durationMs`).
  - *Schema lock*: a lease held by the one instance that is applying migrations.
  - *Validator*: a collection's `$jsonSchema`, enforced with `validationLevel: strict` and `validationAction: error`.
  - To *migrate* is to apply all pending migrations in order.
- **Rules:**
  - Migrations are forward-only; there is no down migration. A mistake is fixed by a new migration.
  - Within a context, sequence numbers start at `0001` and have no gaps or duplicates. Each context migrates independently of the others.
  - A migration is applied at most once, and is recorded in the ledger only after its Up step succeeded.
  - If the ledger contains a migration ID the running code doesn't know (an older binary on a newer database), the server refuses to start.
  - Only one instance migrates at a time. Others wait for the lock, then re-check the ledger. The lock expires automatically if its holder dies (lease of at most 60 s, renewed while migrating).
  - The server only starts serving HTTP after migrations finished. If a migration fails, startup fails.
  - Creating or updating a collection's validator, and creating an index, are idempotent: running them again changes nothing, or applies the new definition.
  - With `STORAGE=mongo`, migrations run before serving. With `STORAGE=memory`, nothing is migrated and no MongoDB connection is made.

### Book catalog (first consumer)

- **Bounded context:** `catalog` (new). This story only adds its storage schema, in `backend/internal/catalog/adapter/mongodb/`, as migration `catalog/0001`.
- **Collection `books`**, one document per book. Every field is validated; any field not listed below is rejected (`additionalProperties: false`).

  | Field | Type | Rules |
  |---|---|---|
  | `_id` | int64 | Book ID. Assigned from a `counters` document `{_id: "books", seq}` |
  | `isbn` | string | ISBN-13: exactly 13 digits, starting with `978` or `979`. Unique |
  | `title` | string | 1–300 characters |
  | `authors` | array of string | 1–20 entries, each 1–200 characters |
  | `publishedYear` | int32 | 1450–2100; optional |
  | `language` | string | ISO 639-1 code: two lowercase letters, e.g. `en`; optional |
  | `pages` | int32 | 1–10000; optional |
  | `createdAt`, `updatedAt` | date | Required |
  | `version` | int64 | ≥ 1, for optimistic concurrency. Required |

- **Indexes:** unique on `isbn`; on `authors`, for lookups by author.
- The ISBN check digit is validated later, by the domain, not by the database.

## Acceptance criteria

**Platform**

- [ ] Given an empty database, when the server starts with `STORAGE=mongo`, then all registered migrations run in order, and each one is recorded in `schema_migrations` with `appliedAt` and `durationMs`.
- [ ] Given every migration is already applied, when the server restarts, then no migration runs again.
- [ ] Given a new migration `catalog/0002` was added, when the server starts, then only `catalog/0002` runs.
- [ ] Given migration `catalog/0002` fails, when the server starts, then startup aborts with an error naming `catalog/0002`, it isn't recorded, `catalog/0001` stays recorded, and after the fix a restart applies `catalog/0002`.
- [ ] Given two server instances start at the same time on an empty database, then every migration is applied exactly once, and both instances end up serving.
- [ ] Given an instance died while holding the schema lock, when another instance starts after the lease expired, then it takes the lock and migrates.
- [ ] Given the ledger contains `catalog/0003` but the code only knows up to `catalog/0002`, when the server starts, then it refuses with an error naming `catalog/0003`.
- [ ] Given a migration set with a duplicate, a gap or an out-of-order sequence number, then it is rejected before anything touches the database. A unit test covers each case.
- [ ] Given the validator and index helpers have already run, when they run again with the same definition, then nothing changes; with a changed validator, the new definition is applied.
- [ ] Given `STORAGE=memory`, when the server starts, then no migration runs and no MongoDB connection is attempted.
- [ ] Each applied migration is logged with its ID, description and duration. `GET /api/health` keeps working.

**Books collection** (each tested by inserting directly with the driver after `catalog/0001` ran)

- [ ] A complete, valid book is accepted, and so is one with only the required fields.
- [ ] A book without `isbn`, `title`, `authors`, `createdAt`, `updatedAt` or `version` is rejected.
- [ ] An `isbn` with 12 or 14 digits, with letters or hyphens, or starting with `977` is rejected.
- [ ] A second book with an existing `isbn` is rejected (unique index).
- [ ] An empty `title`, a 301-character `title`, an empty `authors` array, 21 authors, or an empty author name is rejected.
- [ ] `publishedYear` 1449 or 2101, `pages` 0 or 10001, and `language` `"EN"` or `"eng"` are rejected.
- [ ] A document with an extra field (e.g. `price`) is rejected.
- [ ] The `isbn` (unique) and `authors` indexes exist.

## Notes for planning

- **Fit with the architecture** (`internal/archtest` must stay green):
  - the platform package must not import any bounded context;
  - `internal/catalog/adapter/mongodb` exposes its migration set (`func Migrations() []mongodb.Migration`);
  - `internal/bootstrap` collects all sets and runs the migrator before building the server.
- **Keep the decision logic pure and unit-testable without MongoDB**, test-first. That covers what's pending, sequence validation, and unknown ledger entries. The MongoDB parts (ledger, lock, validators, indexes, the books schema) get integration tests that run when `MONGO_TEST_URI` is set, each in its own database.
- **Order:** the books schema task depends on the platform task, because its migration uses the platform's `Migration` type and validator/index helpers and must compile. Within the platform, the pure planning logic and the MongoDB parts (ledger, lock, helpers) can be built in parallel if their shared types are spelled out in both task prompts. The wiring into `bootstrap` can be part of the books task.

## Out of scope

- The catalog domain model, use cases, repository and API endpoints. They come in the next story, built on this schema.
- ISBN check-digit validation (domain), full-text search on titles.
- Down migrations and rollback.
- Batched backfills of large collections.
- A CLI for migration status or manual runs.
- MongoDB authentication, Atlas and multi-tenant databases.
- Authentication and authorization in the API.
