# Schema migrations

How the MongoDB schema is created and changed. Code: `backend/internal/platform/mongodb/`
(`migration.go`, `ledger.go`, `lock.go`, `schema.go`, `migrate.go`), the catalog's migrations in
`backend/internal/catalog/adapter/mongodb/migrations.go`, and the wiring in
`backend/internal/bootstrap/bootstrap.go`.

## Glossary

| Term | Meaning |
|---|---|
| **Migration** | One forward-only, numbered schema change. Fields: `ID` (`<context>/<NNNN>`, e.g. `catalog/0001`), `Description`, `Up` (the step). |
| **Migration set** | All migrations of one bounded context, in sequence order. Returned by that context's `Migrations()`. |
| **Ledger** | Collection `schema_migrations`. One document per applied migration: `_id` (the migration ID), `description`, `appliedAt` (UTC), `durationMs`. |
| **Schema lock** | Collection `schema_lock`, document `{_id: "schema", owner, expiresAt}`. A lease of at most 60 s (default 30 s), renewed every lease/3 while migrating. |
| **Validator** | `$jsonSchema` set on a collection, with `validationLevel: "strict"` and `validationAction: "error"`: every insert and update must match it. |
| **Migrate** | Apply all pending migrations in order: sets in the order given, each set in sequence order. |

## Rules

- **Forward-only.** There are no down migrations. A mistake is fixed with a new migration.
- **Numbering.** Each context numbers its migrations from `0001`, with no gaps and no duplicates.
  Validation rejects a set that breaks this.
- **Applied at most once.** A migration is recorded in the ledger only after its `Up` succeeded.
  If `Up` succeeds but recording fails, the migration runs again on the next start, so `Up` must be
  idempotent.
- **Unknown IDs stop startup.** If the ledger holds an ID this binary doesn't know, startup fails
  with `unknown migration in ledger`.
- **One instance at a time.** Migrating takes the schema lock. Other instances wait and then re-read
  the ledger.
- **Serve after migrating.** `bootstrap.New` migrates before `cmd/server` starts listening, so HTTP
  is only served once migrations are done.
- **Each context is independent.** A context may appear in only one set.

## Adding a migration

1. In `backend/internal/<context>/adapter/mongodb/`, append the next number to `Migrations()`
   (or create `Migrations()` for a new context).
2. Write the `Up` step with `platformmongo.EnsureCollection` and `platformmongo.EnsureIndexes`.
   Both are idempotent. An index definition can't be changed in place: drop it in a new migration first,
   then create the new one.
3. For a new context, add its set to `migrationSets()` in `bootstrap.go`.
4. Never edit a migration that has already been applied. Databases that ran it won't run it again.

Example, modelled on `catalog/0001`, adding a second index in `catalog/0002`:

```go
func Migrations() []platformmongo.Migration {
	return []platformmongo.Migration{
		{
			ID:          "catalog/0001",
			Description: "create books collection with validator and indexes",
			Up:          createBooks,
		},
		{
			ID:          "catalog/0002",
			Description: "index books by title",
			Up:          indexBooksByTitle,
		},
	}
}

func indexBooksByTitle(ctx context.Context, db *mongo.Database) error {
	return platformmongo.EnsureIndexes(ctx, db, BooksCollection,
		mongo.IndexModel{
			Keys:    bson.D{{Key: "title", Value: 1}},
			Options: options.Index().SetName("title_lookup"),
		},
	)
}
```

To change a validator, keep the old schema in its migration (copy the function rather than editing
it) and pass the new schema to `EnsureCollection`, which applies it with `collMod`.

### Typing pitfalls

- A Go `int` is encoded as BSON **int32** whenever it fits (driver v2 behaviour), and as int64
  otherwise. A `bsonType: "long"` field then rejects it, even for small values.
- Use **`int64`** for `long` fields such as `_id` and `version`.
- Use **`int`** (or `int32`) for `bsonType: "int"` fields such as `publishedYear` and `pages`.
  An `int64` is rejected there.
- Build schemas from `bson.D` and `bson.A`, never `bson.M`, so field order is deterministic.

## The books collection (`catalog/0001`)

Validator: `additionalProperties: false`, so only the fields below are allowed.
Required: `_id`, `isbn`, `title`, `authors`, `createdAt`, `updatedAt`, `version`.

| Field | BSON type | Rules |
|---|---|---|
| `_id` | long | Required. Book ID (int64). |
| `isbn` | string | Required. Matches `^97[89][0-9]{10}$` (ISBN-13). Check digit is validated by the domain. |
| `title` | string | Required. 1–300 characters. |
| `authors` | array of string | Required. 1–20 items, each 1–200 characters. |
| `publishedYear` | int | Optional. 1450–2100. |
| `language` | string | Optional. ISO 639-1, matches `^[a-z]{2}$`. |
| `pages` | int | Optional. 1–10000. |
| `createdAt` | date | Required. |
| `updatedAt` | date | Required. |
| `version` | long | Required. ≥ 1. Optimistic concurrency. |

Indexes:

| Name | Keys | Options |
|---|---|---|
| `isbn_unique` | `{isbn: 1}` | unique |
| `authors_lookup` | `{authors: 1}` | non-unique (multikey) |

The `_id` comment in the code says book IDs come from a `counters` document (`{_id: "books", seq}`).
`catalog/0001` does not create it, and no MongoDB repository exists yet.

## Operations

### What startup logs

Logs use the default `slog` logger (stderr).

```
level=INFO msg="schema migration applied" id=catalog/0001 description="create books collection with validator and indexes" durationMs=42
level=INFO msg=listening addr=:8000 database=orch
```

- One `schema migration applied` line per migration that ran. If nothing is pending, there are no
  migration lines, and the lock isn't taken.
- `level=WARN msg="schema lock not released"`: releasing the lock failed. The lease expires on its own.
- While waiting for another instance's lock, nothing is logged.

### When startup fails

Startup stops with `level=ERROR msg="server stopped" err="migrate database schema: ..."` and exit code 1.

| Error contains | Cause | What to do |
|---|---|---|
| `schema migration <ID> failed` | `Up` returned an error. The migration isn't recorded. | Fix the cause (e.g. data that violates the new validator) and restart. The migration runs again, so its `Up` must be safe to re-run. |
| `unknown migration in ledger: <IDs> (the database is newer than this binary)` | The database has migrations from a newer binary (for example, after a rollback). | Deploy the binary that knows these IDs. Don't delete ledger entries. |
| `schema migration <ID> applied but not recorded: ... (schema lock lost)` | The lock was lost while migrating (a renewal failed). | Restart. The migration runs again, so `Up` must be idempotent. Check for another instance first. |
| Startup hangs with no log line | Another instance holds the lock. | Wait. A dead holder's lock expires after its lease (default 30 s, at most 60 s). A live holder keeps renewing, and startup waits for it. |

To inspect the lock (for example, to confirm the holder is gone):

```js
db.schema_lock.findOne({_id: "schema"})   // {owner, expiresAt}
```

Wait until `expiresAt` has passed. Only delete the document by hand if you have confirmed that no
instance is still running.

### Running the MongoDB tests

Tests need MongoDB, and `backend/check.sh` fails without `MONGO_TEST_URI`.

```sh
make mongo         # starts MongoDB (docker compose)
make check         # runs backend/check.sh with MONGO_TEST_URI (default mongodb://localhost:27017)
```

Each test creates its own database, `test_<random hex>`, and drops it when the test ends. Parallel
agents can share one server.

To run only the platform package tests:

```sh
cd backend && MONGO_TEST_URI=mongodb://localhost:27017 go test ./internal/platform/mongodb/...
```
