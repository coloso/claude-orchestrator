import { afterEach, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, type Router } from 'vue-router'

import type { components } from '@/api/schema'
import App from '@/App.vue'
import { createAppRouter } from '@/router'

enableAutoUnmount(afterEach)

export type Book = components['schemas']['Book']

export function makeBook(overrides: Partial<Book> = {}): Book {
  return {
    id: 7,
    isbn: '9780132350884',
    title: 'Clean Code',
    authors: ['Robert C. Martin'],
    createdAt: '2026-01-02T03:04:05Z',
    updatedAt: '2026-01-02T03:04:05Z',
    version: 1,
    ...overrides,
  }
}

export function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

export function errorResponse(status: number, message: string): Response {
  return jsonResponse(status, { error: message })
}

export function noContent(): Response {
  return new Response(null, { status: 204 })
}

export interface RecordedRequest {
  method: string
  url: URL
  body: unknown
}

type Handler = (req: RecordedRequest) => Response | undefined | Promise<Response | undefined>

/** Replaces the global fetch; returns the live list of requests, in order. */
export function stubFetch(handler: Handler): RecordedRequest[] {
  const requests: RecordedRequest[] = []
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    // Relative URLs (as the app uses) need a base to become a Request.
    const request =
      input instanceof Request ? new Request(input, init) : new Request(new URL(input, 'http://localhost'), init)
    const text = await request.text()
    const recorded: RecordedRequest = {
      method: request.method,
      url: new URL(request.url, 'http://localhost'),
      body: text === '' ? undefined : (JSON.parse(text) as unknown),
    }
    requests.push(recorded)
    const response = await handler(recorded)
    if (response !== undefined) return response
    return errorResponse(
      501,
      `unexpected request in test: ${recorded.method} ${recorded.url.pathname}${recorded.url.search}`,
    )
  })
  return requests
}

export async function renderRoute(path: string): Promise<{ wrapper: VueWrapper; router: Router }> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = createAppRouter(createMemoryHistory())
  await router.push(path)
  await router.isReady()
  const wrapper = mount(App, { global: { plugins: [router, pinia] } })
  await flushPromises()
  return { wrapper, router }
}
