# orch-test: multi-agent orchestration demo

Several headless Claude Code agents build a spec-first Go API in parallel. The spec is OpenAPI 3, and [ogen](https://ogen.dev) generates the server and client code. A Vue + TypeScript frontend comes later.
Each agent works in its own git worktree. A quality gate checks every branch before the orchestrator merges it.

```
orch-test/
├── CLAUDE.md          rules every agent reads
├── backend/           Go 1.27 module                       ┐
│   ├── api/openapi.yaml   API contract (source of truth)   │
│   ├── internal/oas/      ogen-generated server + client   │
│   ├── internal/handler/  implements oas.Handler           │ the product
│   ├── internal/<feature>/ feature services (todos, …)     │ agents work on
│   ├── internal/server/   ogen server + error rendering    │
│   ├── cmd/server/        entry point, :8000               │
│   ├── e2e/               tests via generated oas.Client   │
│   └── check.sh           generate drift + gofmt + vet + test
├── frontend/          reserved for Vue 3 + TS              │
├── docs/              written by the docs agent            ┘
│
├── orchestration/     everything that runs the agents; agents never touch it
│   ├── config.json       workers, model, allowed tools, gates
│   ├── orchestrator/     Python 3 stdlib, no deps
│   │   ├── orchestrator.py   plan / load / run / status / reset; merges passed branches
│   │   ├── worker.py         claim task → `claude -p` in worktree → commit → gate
│   │   ├── tasks.py          file-based queue (atomic rename = claim)
│   │   ├── worktrees.py      one worktree + branch per task
│   │   └── quality_gate.py   scope check + checks for touched areas
│   ├── roles/            role prompts: planner, api, backend, tests, docs, frontend
│   ├── plans/            prepared task plans (todo-tasks.json)
│   └── dashboard/        live view, served by the orchestrator
│
├── .orch/             runtime state (gitignored)
└── .worktrees/T-*/    agent checkouts (gitignored)
```

## Spec-first with ogen

1. The `api` agent edits `api/openapi.yaml` and runs `go generate ./...`. This regenerates `internal/oas`, including the `oas.Handler` interface and a typed client.
2. `handler.Handler` embeds `oas.UnimplementedHandler` one level deeper than the feature services. New operations therefore compile right away and return `501` until a service implements them, so the spec can merge before any implementation.
3. Backend agents implement those operations in `internal/<feature>` and embed the service in `handler.Handler`.
4. Errors are returned as `apperr.NotFound(...)` and similar, then rendered as `{"error": "..."}` by `Handler.NewError`. ogen's schema validation errors use the same format.
5. Test agents call the real server through the generated `oas.Client`, which checks the implementation against the spec.

`check.sh` fails if `internal/oas` doesn't match the spec, which catches hand edits and forgotten regeneration.

## Task lifecycle

```
pending ──claim──▶ claimed ──agent + commit + gate ok──▶ passed ──merge──▶ done
   ▲                  │                                     │
   └──── requeue ──── failed ◀──── gate / agent error ──────┘ (merge conflict)
```

- A task can be claimed once every task in its `depends_on` list is in `done`, so dependent work always starts from merged code.
- The gate rejects any change outside the task's `scope` globs, then runs `backend/check.sh` if the change touched `backend/**`.
- Failed tasks are retried in a fresh worktree until `max_attempts` is reached.
- The orchestrator merges into whatever branch you have checked out, one branch at a time.

The demo plan:

```
T-001 api: todo spec ─────▶ T-003 implement todos ─┬─▶ T-005 e2e tests
                                                   │
T-002 middleware ─────────▶ T-004 wire middleware ─┴─▶ T-006 docs
```

## Run it

Requires `python3`, `go` (1.27+), `git` and `claude` on your PATH. ogen is pinned as a Go tool in `backend/go.mod`, so it doesn't need a separate install.

```bash
# Option A: prepared plan
make demo

# Option B: let the planner agent break down a goal
python3 orchestration/orchestrator/orchestrator.py plan "Add a notes API with tags"
python3 orchestration/orchestrator/orchestrator.py run --workers 3
```

Open http://localhost:8765/orchestration/dashboard/ while it runs. When it finishes, look at the result with `git log --graph --oneline`, then run `make backend`.

### Dry run (no Claude calls, no cost)

`--fake` workers write a placeholder file inside each task's scope and skip the gate commands. That's enough to watch the queue, worktrees, dependency order and merges work. The placeholders get merged, so use a throwaway branch:

```bash
git switch -c dry-run
make dry-run
git switch main && git branch -D dry-run && make reset
```

## Tuning

- `orchestration/config.json`:
  - `max_workers`, `agent.model`, `agent.timeout_s`.
  - `agent.allowed_tools`: agents run with `--permission-mode acceptEdits`, and Bash access is limited to `check.sh` and the `go`/`npm` commands listed there.
- `orchestration/roles/*.md`: role prompts, passed with `--append-system-prompt`.
- Add a role: create `orchestration/roles/<role>.md` and use `"role": "<role>"` in tasks.
- Add a gate: append `{name, paths, cwd, cmd}` to `gates`. The `frontend` gate (`npm run typecheck && npm test`) is already there for when the Vue app exists.
