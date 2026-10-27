import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, type VueWrapper } from '@vue/test-utils'

import type { BookInput, BookUpdate } from '@/api'
import {
  errorResponse,
  jsonResponse,
  makeBook,
  noContent,
  renderRoute,
  stubFetch,
  type Book,
} from '@/__tests__/helpers'

afterEach(() => {
  vi.unstubAllGlobals()
})

type FetchHandler = Parameters<typeof stubFetch>[0]

/** A small in-memory catalog behind the fetch boundary. */
function createCatalog(initial: Book[]) {
  const books = new Map<number, Book>(initial.map((book) => [book.id, book]))
  let nextId = Math.max(0, ...books.keys()) + 1
  const normalizeIsbn = (isbn: string) => isbn.replace(/[-\s]/g, '')

  const handler: FetchHandler = ({ method, url, body }) => {
    if (url.pathname === '/api/books') {
      if (method === 'GET') {
        const author = url.searchParams.get('author')
        const offset = Number(url.searchParams.get('offset') ?? 0)
        const limit = Number(url.searchParams.get('limit') ?? 20)
        const matching = [...books.values()]
          .filter((book) => author === null || book.authors.includes(author))
          .sort((a, b) => a.id - b.id)
        return jsonResponse(200, { items: matching.slice(offset, offset + limit), total: matching.length })
      }
      if (method === 'POST') {
        const input = body as BookInput
        const now = '2026-03-04T05:06:07Z'
        const created: Book = {
          ...input,
          isbn: normalizeIsbn(input.isbn),
          id: nextId++,
          createdAt: now,
          updatedAt: now,
          version: 1,
        }
        books.set(created.id, created)
        return jsonResponse(201, created)
      }
      return undefined
    }

    const match = /^\/api\/books\/(\d+)$/.exec(url.pathname)
    if (match === null) return undefined
    const id = Number(match[1])
    const existing = books.get(id)
    if (existing === undefined) return errorResponse(404, `book ${id} not found`)

    if (method === 'GET') return jsonResponse(200, existing)
    if (method === 'PUT') {
      const { version, ...fields } = body as BookUpdate
      if (version !== existing.version) {
        return errorResponse(409, 'version conflict: the book was changed since it was read')
      }
      const updated: Book = {
        ...fields,
        isbn: normalizeIsbn(fields.isbn),
        id,
        createdAt: existing.createdAt,
        updatedAt: '2026-03-04T05:06:07Z',
        version: existing.version + 1,
      }
      books.set(id, updated)
      return jsonResponse(200, updated)
    }
    if (method === 'DELETE') {
      books.delete(id)
      return noContent()
    }
    return undefined
  }

  return { books, handler }
}

function link(wrapper: VueWrapper, text: string) {
  const found = wrapper.findAll('a').find((a) => a.text() === text)
  if (found === undefined) throw new Error(`no link "${text}"`)
  return found
}

function button(wrapper: VueWrapper, text: string) {
  const found = wrapper.findAll('button').find((b) => b.text() === text)
  if (found === undefined) throw new Error(`no button "${text}"`)
  return found
}

/** The value shown for a term of the details screen's description list. */
function detail(wrapper: VueWrapper, term: string): string {
  const dt = wrapper.findAll('dt').find((d) => d.text() === term)
  const dd = dt?.element.nextElementSibling
  if (dd === null || dd === undefined) throw new Error(`no detail "${term}"`)
  return (dd.textContent ?? '').trim()
}

function noticeText(wrapper: VueWrapper): string | null {
  const notice = wrapper.find('.notice[role="status"]')
  return notice.exists() ? notice.text() : null
}

async function submitForm(wrapper: VueWrapper) {
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

describe('adding a book', () => {
  it('sends only the filled fields and lands on the new book with a normalized ISBN', async () => {
    const catalog = createCatalog([makeBook({ id: 7 })])
    const requests = stubFetch(catalog.handler)
    const { wrapper, router } = await renderRoute('/books/new')

    await wrapper.get('#book-isbn').setValue('978-0-13-235088-4')
    await wrapper.get('#book-title').setValue('The Pragmatic Programmer')
    await wrapper.get('#book-author-1').setValue('Andrew Hunt')
    await button(wrapper, 'Add author').trigger('click')
    await wrapper.get('#book-author-2').setValue('David Thomas')
    await submitForm(wrapper)

    const posts = requests.filter((r) => r.method === 'POST')
    expect(posts).toHaveLength(1)
    expect(posts[0]?.body).toEqual({
      isbn: '978-0-13-235088-4',
      title: 'The Pragmatic Programmer',
      authors: ['Andrew Hunt', 'David Thomas'],
    })
    expect(Object.keys(posts[0]?.body as object).sort()).toEqual(['authors', 'isbn', 'title'])

    await vi.waitFor(() => expect(wrapper.find('dl').exists()).toBe(true))
    expect(router.currentRoute.value.name).toBe('book')
    expect(router.currentRoute.value.params.id).toBe('8')
    expect(wrapper.get('h1').text()).toBe('The Pragmatic Programmer')
    expect(detail(wrapper, 'ISBN')).toBe('9780132350884')
    expect(detail(wrapper, 'Published year')).toBe('—')
    expect(detail(wrapper, 'Language')).toBe('—')
    expect(detail(wrapper, 'Pages')).toBe('—')
  })
})

describe('revising a book', () => {
  it('sends all editable fields with the loaded version and shows version 4', async () => {
    const original = makeBook({
      id: 7,
      title: 'Clean Code',
      authors: ['Robert C. Martin'],
      publishedYear: 2008,
      language: 'en',
      pages: 464,
      version: 3,
    })
    const catalog = createCatalog([original])
    const requests = stubFetch(catalog.handler)
    const { wrapper, router } = await renderRoute('/books/7/edit')

    expect(wrapper.get<HTMLInputElement>('#book-title').element.value).toBe('Clean Code')
    await wrapper.get('#book-title').setValue('Clean Code, 2nd edition')
    await submitForm(wrapper)

    const puts = requests.filter((r) => r.method === 'PUT')
    expect(puts).toHaveLength(1)
    expect(puts[0]?.url.pathname).toBe('/api/books/7')
    expect(puts[0]?.body).toEqual({
      isbn: '9780132350884',
      title: 'Clean Code, 2nd edition',
      authors: ['Robert C. Martin'],
      publishedYear: 2008,
      language: 'en',
      pages: 464,
      version: 3,
    })

    await vi.waitFor(() => expect(wrapper.find('dl').exists()).toBe(true))
    expect(router.currentRoute.value.name).toBe('book')
    expect(router.currentRoute.value.params.id).toBe('7')
    expect(wrapper.get('h1').text()).toBe('Clean Code, 2nd edition')
    expect(detail(wrapper, 'Title')).toBe('Clean Code, 2nd edition')
    expect(detail(wrapper, 'Version')).toBe('4')
  })
})

describe('removing a book', () => {
  async function openBookFromCatalog() {
    const catalog = createCatalog([
      makeBook({ id: 7, title: 'Clean Code' }),
      makeBook({ id: 8, isbn: '9780201616224', title: 'The Pragmatic Programmer' }),
    ])
    const requests = stubFetch(catalog.handler)
    const rendered = await renderRoute('/books')
    await link(rendered.wrapper, 'Clean Code').trigger('click')
    await vi.waitFor(() => expect(rendered.wrapper.find('dl').exists()).toBe(true))
    return { ...rendered, catalog, requests }
  }

  it('asks for confirmation, and Cancel closes it without calling the API', async () => {
    const { wrapper, requests } = await openBookFromCatalog()

    await button(wrapper, 'Delete').trigger('click')
    await flushPromises()
    const group = wrapper.get('[role="group"]')
    expect(group.text()).toContain('Delete "Clean Code"?')

    await button(wrapper, 'Cancel').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="group"]').exists()).toBe(false)
    expect(requests.filter((r) => r.method === 'DELETE')).toHaveLength(0)
  })

  it('deletes the book, returns to the catalog with a notice that goes away on the next navigation', async () => {
    const { wrapper, router, requests } = await openBookFromCatalog()

    await button(wrapper, 'Delete').trigger('click')
    await flushPromises()
    await wrapper.get('[role="group"]').findAll('button').find((b) => b.text() === 'Delete')?.trigger('click')
    await flushPromises()

    const deletes = requests.filter((r) => r.method === 'DELETE')
    expect(deletes).toHaveLength(1)
    expect(deletes[0]?.url.pathname).toBe('/api/books/7')

    await vi.waitFor(() => expect(wrapper.find('table').exists()).toBe(true))
    expect(router.currentRoute.value.name).toBe('books')
    expect(noticeText(wrapper)).toContain('Deleted Clean Code')
    expect(wrapper.get('table').text()).not.toContain('Clean Code')
    expect(wrapper.get('table').text()).toContain('The Pragmatic Programmer')

    await link(wrapper, 'New book').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.name).toBe('book-new')
    expect(noticeText(wrapper)).toBeNull()
  })

  it('also returns to the catalog when the book was already removed', async () => {
    const { wrapper, router, catalog } = await openBookFromCatalog()
    catalog.books.delete(7)

    await button(wrapper, 'Delete').trigger('click')
    await flushPromises()
    await wrapper.get('[role="group"]').findAll('button').find((b) => b.text() === 'Delete')?.trigger('click')
    await flushPromises()

    await vi.waitFor(() => expect(wrapper.find('table').exists()).toBe(true))
    expect(router.currentRoute.value.name).toBe('books')
    expect(wrapper.get('table').text()).not.toContain('Clean Code')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })
})

describe('navigation', () => {
  it('opens the empty form from New book, and Cancel returns to the catalog', async () => {
    const catalog = createCatalog([makeBook({ id: 7 })])
    stubFetch(catalog.handler)
    const { wrapper, router } = await renderRoute('/books')

    await link(wrapper, 'New book').trigger('click')
    await vi.waitFor(() => expect(wrapper.find('#book-isbn').exists()).toBe(true))
    expect(router.currentRoute.value.name).toBe('book-new')
    expect(wrapper.get('h1').text()).toBe('New book')
    for (const id of ['book-isbn', 'book-title', 'book-author-1', 'book-publishedYear', 'book-language', 'book-pages']) {
      expect(wrapper.get<HTMLInputElement>(`#${id}`).element.value).toBe('')
    }

    await link(wrapper, 'Cancel').trigger('click')
    await vi.waitFor(() => expect(wrapper.find('table').exists()).toBe(true))
    expect(router.currentRoute.value.fullPath).toBe('/books')
    expect(wrapper.get('h1').text()).toBe('Catalog')
  })

  it('leads to the catalog from the header link', async () => {
    const catalog = createCatalog([makeBook({ id: 7 })])
    stubFetch(catalog.handler)
    const { wrapper, router } = await renderRoute('/books/new')

    await wrapper.get('header a').trigger('click')
    await vi.waitFor(() => expect(wrapper.find('table').exists()).toBe(true))
    expect(router.currentRoute.value.fullPath).toBe('/books')
  })

  it('redirects / to the catalog', async () => {
    const catalog = createCatalog([makeBook({ id: 7 })])
    stubFetch(catalog.handler)
    const { wrapper, router } = await renderRoute('/')

    await vi.waitFor(() => expect(wrapper.find('table').exists()).toBe(true))
    expect(router.currentRoute.value.fullPath).toBe('/books')
    expect(wrapper.get('h1').text()).toBe('Catalog')
  })

  it('shows "Page not found" for an unknown path, with a link back to the catalog', async () => {
    const catalog = createCatalog([makeBook({ id: 7 })])
    stubFetch(catalog.handler)
    const { wrapper, router } = await renderRoute('/no/such/page')

    expect(wrapper.get('h1').text()).toBe('Page not found')
    const back = wrapper.get('main a')
    expect(back.attributes('href')).toBe('/books')

    await back.trigger('click')
    await vi.waitFor(() => expect(wrapper.find('table').exists()).toBe(true))
    expect(router.currentRoute.value.fullPath).toBe('/books')
  })
})
