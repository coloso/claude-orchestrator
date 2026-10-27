import { beforeEach, vi } from 'vitest'

// No test may reach the network: stubFetch() replaces this per test.
beforeEach(() => {
  vi.stubGlobal('fetch', () =>
    Promise.reject(new TypeError('unexpected network access in test: use stubFetch')),
  )
})
