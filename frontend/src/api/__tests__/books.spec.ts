import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  PAGE_SIZE,
  createBook,
  deleteBook,
  getBook,
  listBooks,
  updateBook,
  type Book,
  type BookList,
} from '@/api'

const book: Book = {
  id: 7,
  isbn: '9780132350884',
  title: 'Clean Code',
  authors: ['Robert C. Martin'],
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
  version: 1,
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** Stubs fetch with one response and returns the mock, whose calls hold the sent Requests. */
function stubFetch(respond: () => Response | Promise<Response>) {
  const mock = vi.fn((_request: Request) => Promise.resolve().then(respond))
  vi.stubGlobal('fetch', mock)
  return mock
}

function sent(mock: ReturnType<typeof stubFetch>): Request {
  const request = mock.mock.calls[0]?.[0]
  if (!request) throw new Error('fetch was not called')
  return request
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('PAGE_SIZE', () => {
  it('is 20', () => {
    expect(PAGE_SIZE).toBe(20)
  })
})

describe('listBooks', () => {
  it('sends author, offset and limit', async () => {
    const list: BookList = { items: [book], total: 1 }
    const fetchMock = stubFetch(() => json(list))

    const result = await listBooks({ author: 'Martin Fowler', offset: 0, limit: 20 })

    const request = sent(fetchMock)
    const url = new URL(request.url)
    expect(request.method).toBe('GET')
    expect(url.pathname).toBe('/api/books')
    expect(url.searchParams.get('author')).toBe('Martin Fowler')
    expect(url.searchParams.get('offset')).toBe('0')
    expect(url.searchParams.get('limit')).toBe('20')
    expect(result).toEqual({ ok: true, data: list })
  })

  it('leaves out an undefined author', async () => {
    const fetchMock = stubFetch(() => json({ items: [], total: 0 }))
    await listBooks({ offset: 20, limit: 20 })
    const url = new URL(sent(fetchMock).url)
    expect(url.searchParams.has('author')).toBe(false)
    expect(url.searchParams.get('offset')).toBe('20')
  })

  it('leaves out an empty author and undefined params', async () => {
    const fetchMock = stubFetch(() => json({ items: [], total: 0 }))
    await listBooks({ author: '', offset: 0 })
    const url = new URL(sent(fetchMock).url)
    expect(url.searchParams.has('author')).toBe(false)
    expect(url.searchParams.has('limit')).toBe(false)
    expect(url.searchParams.get('offset')).toBe('0')
  })
})

describe('getBook', () => {
  it('sends GET /api/books/7', async () => {
    const fetchMock = stubFetch(() => json(book))
    const result = await getBook(7)
    const request = sent(fetchMock)
    expect(request.method).toBe('GET')
    expect(new URL(request.url).pathname).toBe('/api/books/7')
    expect(result).toEqual({ ok: true, data: book })
  })
})

describe('createBook', () => {
  it('sends the body exactly as given', async () => {
    const fetchMock = stubFetch(() => json(book, 201))
    const input = { isbn: '9780132350884', title: 'Clean Code', authors: ['Robert C. Martin'] }

    const result = await createBook(input)

    const request = sent(fetchMock)
    expect(request.method).toBe('POST')
    expect(new URL(request.url).pathname).toBe('/api/books')
    expect(await request.json()).toStrictEqual(input)
    expect(result).toEqual({ ok: true, data: book })
  })

  it('sends optional fields when given', async () => {
    const fetchMock = stubFetch(() => json(book, 201))
    const input = {
      isbn: '9780132350884',
      title: 'Clean Code',
      authors: ['Robert C. Martin'],
      publishedYear: 2008,
      language: 'en',
      pages: 464,
    }
    await createBook(input)
    expect(await sent(fetchMock).json()).toStrictEqual(input)
  })
})

describe('updateBook', () => {
  it('sends PUT /api/books/7 with the version', async () => {
    const fetchMock = stubFetch(() => json({ ...book, version: 2 }))
    const input = {
      isbn: '9780132350884',
      title: 'Clean Code, 2nd ed.',
      authors: ['Robert C. Martin'],
      version: 1,
    }

    const result = await updateBook(7, input)

    const request = sent(fetchMock)
    expect(request.method).toBe('PUT')
    expect(new URL(request.url).pathname).toBe('/api/books/7')
    expect(await request.json()).toStrictEqual(input)
    expect(result).toEqual({ ok: true, data: { ...book, version: 2 } })
  })
})

describe('deleteBook', () => {
  it('gives null data for a 204', async () => {
    const fetchMock = stubFetch(() => new Response(null, { status: 204 }))
    const result = await deleteBook(7)
    const request = sent(fetchMock)
    expect(request.method).toBe('DELETE')
    expect(new URL(request.url).pathname).toBe('/api/books/7')
    expect(result).toEqual({ ok: true, data: null })
  })
})

describe('errors', () => {
  it('classifies a 400 as invalid and keeps the message', async () => {
    const message = 'title: must have 1 to 300 characters'
    stubFetch(() => json({ error: message }, 400))
    expect(await createBook({ isbn: '', title: '', authors: [] })).toEqual({
      ok: false,
      error: { status: 400, kind: 'invalid', message },
    })
  })

  it('classifies a 404 as not-found', async () => {
    stubFetch(() => json({ error: 'book not found' }, 404))
    expect(await getBook(9)).toEqual({
      ok: false,
      error: { status: 404, kind: 'not-found', message: 'book not found' },
    })
  })

  it('classifies a 404 with an empty body as not-found', async () => {
    stubFetch(() => new Response(null, { status: 404, headers: { 'Content-Length': '0' } }))
    expect(await deleteBook(9)).toEqual({
      ok: false,
      error: { status: 404, kind: 'not-found', message: 'Request failed with status 404' },
    })
  })

  it('classifies a version conflict', async () => {
    const message = 'version conflict: the book was changed since it was read'
    stubFetch(() => json({ error: message }, 409))
    const result = await updateBook(7, { isbn: 'x', title: 't', authors: ['a'], version: 1 })
    expect(result).toEqual({ ok: false, error: { status: 409, kind: 'version-conflict', message } })
  })

  it('classifies any other 409 as a duplicate ISBN', async () => {
    const message = 'a book with this ISBN already exists'
    stubFetch(() => json({ error: message }, 409))
    const result = await updateBook(7, { isbn: 'x', title: 't', authors: ['a'], version: 1 })
    expect(result).toEqual({ ok: false, error: { status: 409, kind: 'duplicate-isbn', message } })
  })

  it('classifies a 500 as other', async () => {
    stubFetch(() => json({ error: 'internal server error' }, 500))
    expect(await listBooks({})).toEqual({
      ok: false,
      error: { status: 500, kind: 'other', message: 'internal server error' },
    })
  })

  it('uses a generic message for a text body', async () => {
    stubFetch(() => new Response('Bad Gateway', { status: 502 }))
    expect(await getBook(1)).toEqual({
      ok: false,
      error: { status: 502, kind: 'other', message: 'Request failed with status 502' },
    })
  })

  it('uses a generic message for unexpected JSON', async () => {
    stubFetch(() => json({ detail: 'nope' }, 503))
    expect(await getBook(1)).toEqual({
      ok: false,
      error: { status: 503, kind: 'other', message: 'Request failed with status 503' },
    })
  })

  it('turns a rejected fetch into a network error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const expected = {
      ok: false,
      error: { status: 0, kind: 'network', message: 'Could not reach the server.' },
    }
    expect(await listBooks({ offset: 0 })).toEqual(expected)
    expect(await getBook(1)).toEqual(expected)
    expect(await createBook({ isbn: 'x', title: 't', authors: ['a'] })).toEqual(expected)
    expect(await updateBook(1, { isbn: 'x', title: 't', authors: ['a'], version: 1 })).toEqual(
      expected,
    )
    expect(await deleteBook(1)).toEqual(expected)
  })
})
