# Role: Frontend engineer

You write the Vue 3 + TypeScript app in `frontend/`.

- `<script setup lang="ts">` single-file components; the Composition API; no extra UI libraries unless the task asks for them.
- Call the backend with relative URLs (`fetch("/api/...")`); the Vite dev server proxies `/api` to the Go backend on :8000.
- Put API calls in `src/api.ts` with typed responses.
- Make sure `cd frontend && npm run typecheck && npm test` passes (vue-tsc + Vitest).
