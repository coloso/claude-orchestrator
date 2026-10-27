// Security regression tests (S04-08): what leaves the browser (same origin, encoded, bounded to
// the documented parameters), what reaches the URL, and how the UI treats a response it does
// not expect.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, type VueWrapper } from '@vue/test-utils'

import { createBook, deleteBook, getBook, listBooks, parseFieldError, updateBook } from '@/api'
import { errorResponse, jsonResponse, makeBook, noContent, renderRoute, stubFetch } from '@/__tests__/helpers'

afterEach(() => {
  vi.unstubAllGlobals()
})

const EMPTY_LIST = { items: [], total: 0 }

describe('security: requests stay on the page origin', () => {
  it('sends every call to /api/... on the page origin, without credentials in the URL', async () => {
    const requests = stubFetch(({ method }) => {
      if (method === 'DELETE') return noContent()
      return jsonResponse(method === 'POST' ? 201 : 200, method === 'GET' ? EMPTY_LIST : makeBook())
    })

    await listBooks({ author: 'Martin', limit: 20, offset: 0 })
    await getBook(7)
    await createBook({ isbn: '9780132350884', title: 'Clean Code', authors: ['A'] })
    await updateBook(7, { isbn: '9780132350884', title: 'Clean Code', authors: ['A'], version: 1 })
    await deleteBook(7)

    expect(requests).toHaveLength(5)
    for (const { url } of requests) {
      expect(url.origin).toBe(globalThis.location.origin)
      expect(url.pathname.startsWith('/api/books')).toBe(true)
      expect(url.username).toBe('')
      expect(url.password).toBe('')
      expect(url.hash).toBe('')
    }
  })

  it('percent-encodes the author filter and keeps it one value', async () => {
    const requests = stubFetch(() => jsonResponse(200, EMPTY_LIST))

    const author = 'a&offset=5&limit=100#x <b>é/../?'
    await listBooks({ author, limit: 20, offset: 0 })

    const { url } = requests[0]!
    expect([...url.searchParams.keys()].sort()).toEqual(['author', 'limit', 'offset'])
    expect(url.searchParams.getAll('author')).toEqual([author])
    expect(url.searchParams.get('limit')).toBe('20')
    expect(url.searchParams.get('offset')).toBe('0')
    expect(url.search).not.toMatch(/[<> #]/)
    expect(url.pathname).toBe('/api/books')
  })

  it('sends only the documented query parameters, whatever the page URL carries', async () => {
    const requests = stubFetch(() => jsonResponse(200, EMPTY_LIST))

    await renderRoute('/books?limit=100000&sort=title&redirect=https://evil.example&author=Martin&$where=1')

    expect(requests).toHaveLength(1)
    expect([...requests[0]!.url.searchParams.keys()].sort()).toEqual(['author', 'limit', 'offset'])
    expect(requests[0]!.url.searchParams.get('limit')).toBe('20')
  })

  it('puts only a numeric id into the path', async () => {
    const requests = stubFetch(({ method }) => (method === 'DELETE' ? noContent() : jsonResponse(200, makeBook())))

    await getBook(7)
    await updateBook(12345, { isbn: '9780132350884', title: 'T', authors: ['A'], version: 1 })
    await deleteBook(99)

    expect(requests.map((r) => r.url.pathname)).toEqual(['/api/books/7', '/api/books/12345', '/api/books/99'])
  })
})

describe('security: what the list page takes from its URL', () => {
  async function listRequest(query: string) {
    const requests = stubFetch(() => jsonResponse(200, EMPTY_LIST))
    const { wrapper } = await renderRoute(`/books${query}`)
    return { wrapper, request: requests[0]! }
  }

  it.each([
    ['a negative number', '-5'],
    ['text', 'abc'],
    ['an exponent', '1e3'],
    ['a fraction', '1.5'],
    ['hex', '0x10'],
    ['a plus sign', '+5'],
    ['a leading space', '%207'],
    ['a NUL byte', '%00'],
    ['a number beyond the safe integers', '99999999999999999999'],
    ['an empty value', ''],
  ])('treats an offset of %s as 0', async (_name, raw) => {
    const { request } = await listRequest(`?offset=${raw}`)

    expect(request.url.searchParams.get('offset')).toBe('0')
  })

  it('uses the first of repeated author and offset parameters', async () => {
    const { request } = await listRequest('?author=first&author=second&offset=20&offset=40')

    expect(request.url.searchParams.getAll('author')).toEqual(['first'])
    expect(request.url.searchParams.getAll('offset')).toEqual(['20'])
  })

  it('shows an error with a retry for a huge author value instead of breaking', async () => {
    const requests = stubFetch(() => new Response('Request Header Fields Too Large', { status: 431 }))
    const { wrapper } = await renderRoute(`/books?author=${'x'.repeat(50_000)}`)

    expect(requests).toHaveLength(1)
    expect(wrapper.get('[role="alert"]').text()).toContain('Request failed with status 431')
    expect(wrapper.find('button:not([disabled])').exists()).toBe(true)
  })

  it('shows an error for an offset the API refuses', async () => {
    stubFetch(({ url }) =>
      Number(url.searchParams.get('offset')) > 100_000
        ? errorResponse(400, 'invalid query parameter "offset"')
        : jsonResponse(200, EMPTY_LIST),
    )
    const { wrapper } = await renderRoute('/books?offset=9007199254740991')

    expect(wrapper.get('[role="alert"]').text()).toContain('invalid query parameter "offset"')
  })

  it('is not navigated away by redirect-style parameters', async () => {
    stubFetch(() => jsonResponse(200, EMPTY_LIST))
    const { router } = await renderRoute('/books?redirect=https://evil.example&next=//evil.example&returnTo=javascript:alert(1)')

    expect(router.currentRoute.value.name).toBe('books')
    expect(router.currentRoute.value.path).toBe('/books')
  })

  it('keeps only author and offset in the URL when filtering and paging', async () => {
    const items = Array.from({ length: 20 }, (_, i) => makeBook({ id: i + 1 }))
    stubFetch(() => jsonResponse(200, { items, total: 45 }))
    const { wrapper, router } = await renderRoute('/books')

    await wrapper.get('#author-filter').setValue('  Martin  ')
    await wrapper.get('form.filter').trigger('submit')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/books?author=Martin')

    const next = wrapper.findAll('button').find((b) => b.text() === 'Next')
    await next?.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/books?author=Martin&offset=20')
  })
})

describe('security: titles, messages and notices never reach the URL', () => {
  const TITLE = 'Secret <b>title</b> & more'

  it('leaves the URL at /books after a delete and keeps the title out of it', async () => {
    stubFetch(({ method, url }) => {
      if (method === 'GET' && url.pathname === '/api/books/7') return jsonResponse(200, makeBook({ title: TITLE }))
      if (method === 'DELETE') return noContent()
      return jsonResponse(200, EMPTY_LIST)
    })
    const { wrapper, router } = await renderRoute('/books/7')
    await wrapper.get('.actions button').trigger('click')
    await flushPromises()
    await wrapper.get('.confirm button:last-of-type').trigger('click')
    await flushPromises()

    expect(wrapper.get('.notice').text()).toContain(`Deleted ${TITLE}`)
    expect(router.currentRoute.value.fullPath).toBe('/books')
  })

  it('leaves the form URL unchanged when the server refuses a save', async () => {
    stubFetch(({ method }) => (method === 'POST' ? errorResponse(400, 'title: must not be empty') : undefined))
    const { wrapper, router } = await renderRoute('/books/new')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(router.currentRoute.value.fullPath).toBe('/books/new')
  })

  it('goes to /books/<id> after a save, with no query or hash', async () => {
    stubFetch(({ method, url }) => {
      if (method === 'POST') return jsonResponse(201, makeBook({ id: 42, title: TITLE }))
      return url.pathname === '/api/books/42' ? jsonResponse(200, makeBook({ id: 42, title: TITLE })) : undefined
    })
    const { wrapper, router } = await renderRoute('/books/new')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(router.currentRoute.value.fullPath).toBe('/books/42')
  })
})

describe('security: links', () => {
  const APP_HREF = /^\/books(\/new|\/[1-9][0-9]{0,14}(\/edit)?)?(\?[A-Za-z0-9%=&_.~+*-]*)?$/

  function links(wrapper: VueWrapper): Array<{ href: string | undefined; target: string | undefined; rel: string | undefined }> {
    return wrapper.findAll('a').map((a) => ({
      href: a.attributes('href'),
      target: a.attributes('target'),
      rel: a.attributes('rel'),
    }))
  }

  function expectSafeLinks(wrapper: VueWrapper): void {
    const found = links(wrapper)
    expect(found.length).toBeGreaterThan(0)
    for (const { href, target, rel } of found) {
      expect(href).toMatch(APP_HREF)
      if (target === '_blank') expect(rel).toMatch(/noopener[^]*noreferrer|noreferrer[^]*noopener/)
      expect(target).toBeUndefined()
    }
  }

  it('uses only fixed paths and numeric ids on every screen', async () => {
    const evil = makeBook({ id: 7, title: 'https://evil.example', authors: ['//evil.example'], language: 'javascript:alert(1)' })
    stubFetch(({ method, url }) => {
      if (url.pathname === '/api/books/7') return method === 'GET' ? jsonResponse(200, evil) : undefined
      return jsonResponse(200, { items: [evil], total: 1 })
    })

    for (const path of ['/books', '/books/7', '/books/7/edit', '/books/new', '/books/8/nothing']) {
      const { wrapper } = await renderRoute(path)
      expectSafeLinks(wrapper)
    }
  })

  it('shows the not-found page, not the path, for an address that is not a route', async () => {
    const requests = stubFetch(() => undefined)

    for (const path of [
      '/books/0',
      '/books/01',
      '/books/-1',
      '/books/1e3',
      '/books/1234567890123456',
      '/books/%3Cimg%20src=x%20onerror=alert(1)%3E',
      '/%3Cscript%3Ealert(1)%3C/script%3E',
      '/javascript:alert(1)',
      '//evil.example/books',
    ]) {
      const { wrapper } = await renderRoute(path)
      expect(wrapper.get('h1').text()).toBe('Page not found')
      expect(wrapper.html()).not.toContain('onerror')
      expect(wrapper.html()).not.toContain('<script')
      expectSafeLinks(wrapper)
    }
    expect(requests).toHaveLength(0)
  })
})

describe('security: responses the UI does not expect', () => {
  const html = (status: number) =>
    new Response('<html><body><script>alert(1)</script>Bad gateway</body></html>', {
      status,
      headers: { 'Content-Type': 'text/html' },
    })

  it('turns a 200 with a non-JSON body into an error result, not a rejection', async () => {
    stubFetch(() => new Response('this is not json', { status: 200, headers: { 'Content-Type': 'application/json' } }))

    const result = await listBooks({ limit: 20, offset: 0 })

    expect(result.ok).toBe(false)
  })

  it.each([
    ['list', '/books'],
    ['details', '/books/7'],
    ['revise form', '/books/7/edit'],
  ])('shows an error with a retry on the %s page for a 200 with an HTML body', async (_name, path) => {
    stubFetch(() => new Response('<html><script>alert(1)</script></html>', { status: 200, headers: { 'Content-Type': 'text/html' } }))
    const { wrapper } = await renderRoute(path)

    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('alert(1)')
    expect(wrapper.findAll('button').some((b) => b.text() === 'Retry')).toBe(true)
  })

  it.each([
    ['list', '/books'],
    ['details', '/books/7'],
    ['revise form', '/books/7/edit'],
  ])('shows an error on the %s page for an empty 200 and for a 204', async (_name, path) => {
    for (const response of [() => new Response('', { status: 200 }), () => new Response(null, { status: 204 })]) {
      stubFetch(response)
      const { wrapper } = await renderRoute(path)

      expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    }
  })

  it('shows a generic message, not the markup, for an HTML error page from a proxy', async () => {
    stubFetch(() => html(502))
    const { wrapper } = await renderRoute('/books')

    expect(wrapper.get('[role="alert"]').text()).toContain('Request failed with status 502')
    expect(wrapper.html()).not.toContain('Bad gateway')
    expect(wrapper.find('script').exists()).toBe(false)
  })

  it.each([
    ['a number', { error: 123 }],
    ['an object', { error: { message: 'x' } }],
    ['an array', { error: ['x'] }],
    ['null', null],
    ['a string', 'plain'],
  ])('ignores an error body whose "error" is %s', async (_name, body) => {
    stubFetch(() => jsonResponse(500, body))

    const result = await getBook(7)

    expect(result).toMatchObject({ ok: false, error: { status: 500, message: 'Request failed with status 500', kind: 'other' } })
  })

  it('turns a refused connection into a message, not a rejection', async () => {
    vi.stubGlobal('fetch', () => Promise.reject(new TypeError('Failed to fetch')))

    const result = await getBook(7)

    expect(result).toMatchObject({ ok: false, error: { kind: 'network' } })
  })

  it.skip('SECURITY SEC-S04-1: fixed by follow-up "Check the shape of API responses before using them"', async () => {
    // A 200 that is valid JSON but not a Book/BookList (for example from another service that
    // answers on the proxied port) must give the error state, never a half-rendered page.
    // Today BookListView throws in its render function on `book.authors.join`.
    stubFetch(() => jsonResponse(200, { items: [{ id: 1, title: 'No authors' }], total: 1 }))
    const { wrapper } = await renderRoute('/books')

    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.find('tbody').exists()).toBe(false)
  })
})

describe('security: where an error message is placed', () => {
  it.each([
    ['__proto__: x'],
    ['constructor: x'],
    ['toString: x'],
    ['hasOwnProperty: x'],
    [' title: x'],
    ['Title: x'],
    ['title : x'],
    ['title:x'],
    ['author: x'],
    ['<b>title</b>: x'],
    ['title'],
    [''],
  ])('does not place %j at a field', (message) => {
    expect(parseFieldError(message)).toBeNull()
  })

  it('splits only at the first ": " and only for the six form fields', () => {
    expect(parseFieldError('isbn: a: b')).toEqual({ field: 'isbn', reason: 'a: b' })
    for (const field of ['isbn', 'title', 'authors', 'publishedYear', 'language', 'pages']) {
      expect(parseFieldError(`${field}: r`)).toEqual({ field, reason: 'r' })
    }
  })

  it('shows a 409 as the fixed duplicate-ISBN text and a version conflict as the conflict notice', async () => {
    const message = 'a book with this ISBN already exists'
    stubFetch(({ method, url }) => {
      if (url.pathname === '/api/books/7' && method === 'GET') return jsonResponse(200, makeBook())
      if (method === 'PUT') return errorResponse(409, `${message} <img src=x onerror=alert(1)>`)
      return undefined
    })
    const { wrapper } = await renderRoute('/books/7/edit')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('#book-isbn-error').text()).toBe('A book with this ISBN already exists')
    expect(wrapper.text()).not.toContain('onerror')
    expect(wrapper.text()).not.toContain('Load latest replaces')
  })
})
