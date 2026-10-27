# Frontend

The web UI for the book catalog: a Vue 3 + TypeScript app that browses, adds, revises and removes books through the `catalog` API.

## Commands

Run these in `frontend/`, after `npm install` (Node `^22.18.0 || >=24.12.0`):

| Command | What it does |
| --- | --- |
| `npm install` | Installs the dependencies. |
| `npm run dev` | Starts Vite on http://localhost:5173, proxying `/api` to the backend on :8000. Regenerates the API types first. From the repo root: `make frontend`. |
| `npm run generate` | Generates `src/api/schema.d.ts` from `../backend/api/openapi.yaml`. |
| `npm run typecheck` | Regenerates the types, then runs `vue-tsc`. |
| `npm test` | Runs the Vitest suite once. |
| `npm run build` | Type-checks, then builds to `dist/`. |
| `npm run lint` | Runs oxlint and ESLint with `--fix`, so it edits files. |

The frontend gate is `npm run typecheck && npm test`.

## More

See [../docs/frontend.md](../docs/frontend.md) for the screens and routes, how the UI maps to the API, error handling, the tests and the source layout.
