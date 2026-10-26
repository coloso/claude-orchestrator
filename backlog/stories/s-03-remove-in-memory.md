# S-03: Remove the in-memory storage, MongoDB only

## Story

As a backend developer, I want the API to have exactly one storage, MongoDB, and every test to run against it, so that what the tests check is what runs in production: the schema validator, the unique indexes, the counters and optimistic locking. Then no behavior exists only in a second implementation that nobody ships.

Today `STORAGE=memory` is the default, and the e2e tests run against the in-memory repository. MongoDB is only tested when `MONGO_TEST_URI` happens to be set, so a run without it passes without testing the real storage. That ends with this story. **The MongoDB storage itself stays as it is**: its adapter, migrations, schema and behavior don't change.

## Domain

- **Bounded context:** `catalog` (existing). No domain or API change. This story changes the composition root, the tests and the docs.
- **Terms:**
  - *Storage*: where repositories keep their data. After this story there is only one, MongoDB, so there is no setting to choose it.
  - *Test database*: a MongoDB database created for one test and dropped when it ends, on the server named by `MONGO_TEST_URI`.
  - *Test double*: a fake repository inside a test file, used by application-layer unit tests. It is not a storage and isn't wired anywhere.
- **Rules:**
  - The server always connects to MongoDB and runs the migrations before serving. There is no storage setting any more: `STORAGE` is gone from the code, `docker-compose.yml`, the Makefile and the docs, and the server ignores it if set.
  - `MONGO_URI` defaults to `mongodb://localhost:27017` and `MONGO_DB` to `orch`, so `make mongo && make backend` works without setting anything.
  - If MongoDB can't be reached at startup, the server exits with an error naming the URI (without credentials), and it never starts serving.
  - `backend/check.sh` **requires** `MONGO_TEST_URI`. Without it, it fails right at the start with a message saying how to fix it (`make mongo`, then `export MONGO_TEST_URI=mongodb://localhost:27017`). No test skips because MongoDB is missing.
  - Each test that needs MongoDB uses its own test database, dropped when it ends, because parallel agents share one server.
  - Domain and application unit tests still run without MongoDB. They use test doubles defined in their own `_test.go` files, not an adapter.

## Acceptance criteria

**Storage**

- [ ] `backend/internal/catalog/adapter/memory/` no longer exists, and no Go code imports it.
- [ ] `bootstrap.Config` has no storage field, and `bootstrap.ConfigFromEnv` doesn't read `STORAGE`. `bootstrap.New` always connects to MongoDB and migrates before it returns.
- [ ] Given `MONGO_URI` and `MONGO_DB` are unset, then the server connects to `mongodb://localhost:27017` and uses database `orch`.
- [ ] Given MongoDB is unreachable, when the server starts, then it exits with a non-zero status and an error naming the URI, within the connect timeout. Credentials in the URI are not printed.
- [ ] Given `STORAGE=memory` is still set in someone's environment, when the server starts, then it ignores the setting and uses MongoDB.
- [ ] `SEED_DEMO_DATA`, rate limits, `GET /api/health` and every `/api/books` operation behave exactly as before.

**Tests**

- [ ] Given `MONGO_TEST_URI` is unset, when `backend/check.sh` runs, then it exits non-zero before running any test, with a message naming `make mongo` and `MONGO_TEST_URI`.
- [ ] Given `MONGO_TEST_URI` is set, then `backend/check.sh` passes, and every e2e test (CRUD, seeding, storage and every `security_*` test) runs against MongoDB. Nothing is skipped because MongoDB is missing.
- [ ] The e2e suite has one shared helper that wires the app on a fresh test database and drops it when the test ends. It replaces `newClient`'s in-memory wiring and the per-file copies of the "own database" helper. Tests that ran once per backend (e.g. through `secEachBackend`) run once, against MongoDB.
- [ ] The repository contract suite (`adapter/repotest`) still exists and runs against the MongoDB repository.
- [ ] `catalog/app` unit tests (`service_test.go`, `seed_test.go`) use a test double in the `app` package's test files, and coverage of `catalog/app` and `catalog/domain` stays at 80% or more.
- [ ] The e2e test that checked "`STORAGE=memory` makes no MongoDB connection" is replaced by one for the unreachable-MongoDB criterion above.
- [ ] Security regression tests keep the secure behavior they pin. A test whose only purpose was comparing memory with MongoDB may go, but its MongoDB assertion stays.
- [ ] `internal/archtest` passes unchanged.

**Docs and tooling**

- [ ] `CLAUDE.md`, `README.md` and `docs/schema-migrations.md` no longer mention the memory storage or `STORAGE`. They say that tests need MongoDB (`make mongo`, then `export MONGO_TEST_URI=…`), and the architecture tree in `CLAUDE.md` lists no `adapter/memory/`.
- [ ] `make backend` runs the API against MongoDB, and its help text says so. `make check` and `make test-mongo` both pass `MONGO_TEST_URI`, or one of them is removed.
- [ ] `docker-compose.yml` no longer sets `STORAGE`. `docker compose up` still becomes healthy.

## Notes for planning

- **Order:** the test infrastructure has to come first. Build the shared e2e helper and the `app` test doubles, then switch the e2e suite to MongoDB, and only then remove the adapter and the setting from `bootstrap`. Otherwise the build or the tests break in between. Docs come last.
- **Scopes:** `backend/e2e/**` is large: 9 test files, and the two `security_*` files have over 1,000 lines each. Split it across tasks by file group (CRUD and seeding; storage and schema migrations; security tests), all depending on the shared-helper task. That task owns the helper's file, so the others don't edit it.
- **Test runtime:** every e2e test now creates and drops a database. Keep that cheap: create the database lazily, drop it in `t.Cleanup`, and let tests run in parallel where they already do.
- **Gate:** from this story on, every task's gate needs `MONGO_TEST_URI`. Start `make run` in a terminal where it is exported.
- **Human follow-ups outside the backend** (agents don't change `orchestration/`): `orchestration/roles/*.md` mention the memory adapter, and `make run` should refuse to start without `MONGO_TEST_URI` instead of only warning.

## Out of scope

- Any change to the MongoDB adapter, its migrations, the `books` schema or the indexes.
- A new storage (SQL, an embedded database) or a storage interface beyond `domain.Repository`.
- Testcontainers or starting MongoDB from the tests. The tests use the server named by `MONGO_TEST_URI`.
- MongoDB authentication, Atlas, replica sets.
- Authentication and authorization in the API.
