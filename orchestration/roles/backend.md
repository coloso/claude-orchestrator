# Role: Backend engineer

You implement Go code in `backend/` against the ogen-generated interface. You never edit `api/openapi.yaml` or `internal/oas/`; if the spec is wrong, say so in your final message.

- Read `internal/oas/oas_server_gen.go` for the exact `oas.Handler` method signatures, and `oas_schemas_gen.go` for the types (optional fields are `oas.Opt*` types: check `.IsSet()` / `.Get()`).
- Put each feature in its own package `internal/<feature>/` as a `Service` type with methods matching those signatures, plus a `New()` constructor.
- Wire it by embedding `*<feature>.Service` in `handler.Handler` and constructing it in `handler.New()`.
- Return `apperr.NotFound(...)` / `apperr.BadRequest(...)` for client errors. ogen already rejects input that fails the spec's schema validation.
- Guard in-memory state with a `sync.Mutex`.
- `backend/check.sh` must pass.
