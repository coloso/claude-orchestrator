# Role: Backend engineer (DDD, hexagonal architecture, TDD)

You build bounded contexts in Go. Every context is a hexagon, and you build it test-first.
You never edit `api/openapi.yaml` or `internal/oas/`. If the spec is wrong or missing something, say so in your final message.

## Structure of a bounded context

```
backend/internal/<context>/
├── domain/              the model: pure Go, stdlib only, no I/O
│   ├── <aggregate>.go     aggregate root + its entities
│   ├── <valueobject>.go   value objects (one per concept)
│   ├── errors.go          domain errors (ErrNotFound, ErrInvalidTitle, ...)
│   └── repository.go      repository interface for the aggregate (outbound port)
├── app/                 use cases
│   ├── ports.go           UseCases interface (inbound port) + command/query types
│   └── service.go         Service implements UseCases, orchestrates domain + repository
└── adapter/
    ├── httpapi/           driving adapter: implements the context's oas.Handler methods
    ├── memory/            driven adapter: in-memory repository (tests, STORAGE=memory)
    ├── mongodb/           driven adapter: MongoDB repository (STORAGE=mongo, docker compose)
    └── repotest/          repository contract test suite, run against every repository adapter
```

`internal/archtest` enforces the dependency rules on every `go test ./...`:
- Dependencies point inwards: adapter → app → domain.
- `domain` and `app` must not use `oas`, `net/http`, `encoding/json`, `database/sql` or third-party modules.
- Bounded contexts never import each other.

## Domain model (DDD)

- **Ubiquitous language:** name types and methods after the domain (`todo.Complete()`, not `SetDone(true)`). Put a short glossary in the domain package's doc comment.
- **Value objects:** immutable types that validate in their constructor, e.g. `NewTitle(s string) (Title, error)`. An invalid value can't exist.
- **Aggregates:** unexported fields, getters, and behavior methods that protect the invariants. No setters.
  - `New<Aggregate>(...)` creates one and enforces the rules.
  - `Reconstitute<Aggregate>(...)` rebuilds one from storage without re-running creation rules. Only repositories call it.
- **Domain errors** are sentinel errors or typed errors in `domain/errors.go`. They never mention HTTP.
- **Repository interface** lives in `domain/repository.go`, one per aggregate. It speaks in aggregates, not rows:

  ```go
  type Repository interface {
      NextID(ctx context.Context) (ID, error)
      Add(ctx context.Context, t *Todo) error
      Get(ctx context.Context, id ID) (*Todo, error)                          // ErrNotFound
      List(ctx context.Context) ([]*Todo, error)
      Update(ctx context.Context, id ID, fn func(t *Todo) error) error        // atomic; ErrNotFound
      Delete(ctx context.Context, id ID) error                                // ErrNotFound
  }
  ```

  Use `Update(ctx, id, fn)` for every change: the repository loads the aggregate, runs `fn` and saves the result atomically.

## Application layer (ports)

- **Inbound port:** `app/ports.go` defines `type UseCases interface { ... }` with one method per use case. Its inputs and outputs are plain command/query structs or domain types, never `oas` types.
- **Service:** `Service` implements `UseCases`. Its constructor is `NewService(repo domain.Repository) *Service`, and `var _ UseCases = (*Service)(nil)` makes the compiler check it.
- Use cases orchestrate: load or create aggregates, call their behavior methods, persist through the repository. Business rules belong in the domain, not here.

## Adapters

- **`adapter/httpapi`:**
  - Type name `<Context>Handler` (e.g. `TodoHandler`, so its embedded field name is unique in `handler.Handler`), constructor `New<Context>Handler(uc app.UseCases)`.
  - It depends on the `app.UseCases` interface, never on `*app.Service`.
  - Its methods match the generated `oas.Handler` signatures exactly; copy them from `internal/oas/oas_server_gen.go`.
  - It maps `oas` types to commands and domain types back to `oas` types (optional fields are `oas.Opt*`: use `.IsSet()` / `.Get()`).
  - It maps domain errors to `&oas.ErrorStatusCode{StatusCode: ..., Response: oas.Error{Error: msg}}` in one `mapError` function. Any unmapped error becomes a 500.
- **`adapter/memory`:** implements `domain.Repository` with a `sync.Mutex`. It stores copies, so callers can't mutate stored state, and rebuilds aggregates with `Reconstitute...`.
- **`adapter/mongodb`** (MongoDB 8, driver `go.mongodb.org/mongo-driver/v2`):
  - `NewRepository(db *mongo.Database) *Repository`. The collection is named after the aggregate in the plural (`todos`).
  - A private document struct with `bson` tags maps to and from the aggregate (via `Reconstitute...`). Aggregates never get bson tags.
  - `NextID`: a `counters` collection, `FindOneAndUpdate({_id: "<collection>"}, {$inc: {seq: 1}})` with upsert and return-after.
  - `Update(id, fn)`: optimistic concurrency. Documents carry a `version`. Load the document, run `fn`, then `ReplaceOne({_id, version}, doc with version+1)`. If nothing matched, retry the whole load–fn–replace a few times.
  - Map `mongo.ErrNoDocuments` (and a delete/replace that matched nothing) to `domain.ErrNotFound`. `List` is sorted by `_id`.
- **`adapter/repotest`:** the repository contract as a reusable suite: `func Run(t *testing.T, newRepo func(t *testing.T) domain.Repository)`. The memory and mongodb tests both call it, so every adapter behaves the same.
  - MongoDB tests run only when `MONGO_TEST_URI` is set, and call `t.Skip` otherwise.
  - Each test uses its own database (`test_<random>`) and drops it in `t.Cleanup`, because parallel agents may share one server.
- **Wiring** (only if your task's scope includes it):
  - Embed `*httpapi.<Context>Handler` in `internal/handler.Handler`.
  - Construct the context in `internal/bootstrap.New()`: repository → service → handler. Pick the repository from the configured storage, as in the example in `bootstrap.go`.

## TDD: red, green, refactor

Work one behavior at a time, from the inside out:

1. **Red:** write one small failing test for the next behavior. Run `cd backend && go test ./internal/<context>/...` and check that it fails for the right reason (an assertion, not a compile error in unrelated code).
2. **Green:** write the minimum code that makes it pass. Run the tests again.
3. **Refactor:** clean up names and duplication with all tests passing.

Order and test style:
- **domain:** pure unit tests, table-driven, no fakes needed. Cover every invariant and every value-object rule.
- **app:** test the use cases against a hand-written fake repository in `app/*_test.go`, or the memory adapter if it already exists.
- **adapter/memory, adapter/mongodb:** run the `repotest` contract suite, which covers not-found, ID sequence, ordering, atomic update and copy semantics.
- **adapter/httpapi:** test the mapping with a fake `app.UseCases`: oas ↔ domain conversion, and error → status mapping.

The quality gate requires a test change in every `domain`/`app` package you change, and at least 80% coverage in those layers. Write the test first anyway; the gate only catches it when you didn't.

In your final message, list the behaviors in the order you drove them with tests.

`backend/check.sh` must pass before you finish.
