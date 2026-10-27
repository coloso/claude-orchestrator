import createClient from 'openapi-fetch'
import type { paths } from './schema'

// fetch is looked up on every call (openapi-fetch would otherwise capture it once), and the
// base URL is the page's own origin: in dev that is Vite, which proxies /api to the backend.
export const client = createClient<paths>({
  baseUrl: globalThis.location.origin,
  fetch: (request: Request) => globalThis.fetch(request),
})
