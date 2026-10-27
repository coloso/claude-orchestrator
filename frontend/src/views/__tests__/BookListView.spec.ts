import { describe, expect, it } from 'vitest'
import { flushPromises, type VueWrapper } from '@vue/test-utils'

import {
  errorResponse,
  jsonResponse,
  makeBook,
  renderRoute,
  stubFetch,
  type Book,
  type RecordedRequest,
} from '@/__tests__/helpers'

const FOWLER = 'Martin Fowler'

function makeCatalog(count: number, byFowler: (id: number) => boolean = (id) => id % 5 === 0): Book[] {
  return Array.from({ length: count }, (_, i) => {
    const id = i + 1
    return makeBook({ id, title: `Book ${id}`, authors: [byFowler(id) ? FOWLER : 'Someone Else'] })
  })
}

function listHandler(books: Book[]) {
  return (req: RecordedRequest): Response | undefined => {
    if (req.method !== 'GET' || req.url.pathname !== '/api/books') return undefined
    const params = req.url.searchParams
    const author = params.get('author')
    const matching = books.filter((b) => author === null || b.authors.includes(author))
    const offset = Number(params.get('offset') ?? 0)
    const limit = Number(params.get('limit') ?? 20)
    return jsonResponse(200, { items: matching.slice(offset, offset + limit), total: matching.length })
  }
}

function titles(wrapper: VueWrapper): string[] {
  return wrapper.findAll('tbody tr').map((row) => row.get('a').text())
}

function button(wrapper: VueWrapper, name: string) {
  const found = wrapper.findAll('button').find((b) => b.text() === name)
  if (found === undefined) throw new Error(`no button "${name}"`)
  return found
}

function hasButton(wrapper: VueWrapper, name: string): boolean {
  return wrapper.findAll('button').some((b) => b.text() === name)
}

function authorInput(wrapper: VueWrapper): HTMLInputElement {
  return wrapper.get('input[type="text"]').element as HTMLInputElement
}

function nth<T>(items: T[], index: number): T {
  const item = items[index < 0 ? items.length + index : index]
  if (item === undefined) throw new Error(`no item at ${index}`)
  return item
}

function failWithNetworkError(): Response {
  throw new TypeError('Failed to fetch')
}

describe('BookListView', () => {
  it('shows the first 20 books and pages to the rest', async () => {
    const requests = stubFetch(listHandler(makeCatalog(25)))
    const { wrapper, router } = await renderRoute('/books')

    expect(nth(requests, 0).url.searchParams.get('offset')).toBe('0')
    expect(nth(requests, 0).url.searchParams.get('limit')).toBe('20')
    expect(nth(requests, 0).url.searchParams.has('author')).toBe(false)
    expect(titles(wrapper)).toEqual(Array.from({ length: 20 }, (_, i) => `Book ${i + 1}`))
    expect(wrapper.text()).toContain('1–20 of 25')

    await button(wrapper, 'Next').trigger('click')
    await flushPromises()

    expect(titles(wrapper)).toEqual(['Book 21', 'Book 22', 'Book 23', 'Book 24', 'Book 25'])
    expect(wrapper.text()).toContain('21–25 of 25')
    expect(router.currentRoute.value.query).toEqual({ offset: '20' })
  })

  it('disables Previous on the first page and Next on the last', async () => {
    stubFetch(listHandler(makeCatalog(25)))
    const { wrapper } = await renderRoute('/books')
    expect(button(wrapper, 'Previous').attributes('disabled')).toBeDefined()
    expect(button(wrapper, 'Next').attributes('disabled')).toBeUndefined()

    await button(wrapper, 'Next').trigger('click')
    await flushPromises()
    expect(button(wrapper, 'Previous').attributes('disabled')).toBeUndefined()
    expect(button(wrapper, 'Next').attributes('disabled')).toBeDefined()
  })

  it('returns to the first page with Previous and drops the offset', async () => {
    stubFetch(listHandler(makeCatalog(25)))
    const { wrapper, router } = await renderRoute('/books?offset=20')
    expect(wrapper.text()).toContain('21–25 of 25')

    await button(wrapper, 'Previous').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({})
    expect(wrapper.text()).toContain('1–20 of 25')
  })

  it('filters by author and clears the filter', async () => {
    const requests = stubFetch(listHandler(makeCatalog(25)))
    const { wrapper, router } = await renderRoute('/books')

    await wrapper.get('input[type="text"]').setValue(`  ${FOWLER} `)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    const last = nth(requests, -1)
    expect(last.url.searchParams.get('author')).toBe(FOWLER)
    expect(last.url.searchParams.get('offset')).toBe('0')
    expect(router.currentRoute.value.query).toEqual({ author: FOWLER })
    expect(titles(wrapper)).toEqual(['Book 5', 'Book 10', 'Book 15', 'Book 20', 'Book 25'])
    expect(wrapper.text()).toContain('1–5 of 5')

    await button(wrapper, 'Clear filter').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({})
    expect(nth(requests, -1).url.searchParams.has('author')).toBe(false)
    expect(wrapper.text()).toContain('1–20 of 25')
    expect(authorInput(wrapper).value).toBe('')
    expect(hasButton(wrapper, 'Clear filter')).toBe(false)
  })

  it('submitting an empty filter clears it', async () => {
    stubFetch(listHandler(makeCatalog(25)))
    const { wrapper, router } = await renderRoute(`/books?author=${FOWLER}&offset=0`)

    await wrapper.get('input[type="text"]').setValue('   ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(router.currentRoute.value.fullPath).toBe('/books')
  })

  it('filtering goes back to the first page', async () => {
    stubFetch(listHandler(makeCatalog(60, () => true)))
    const { wrapper, router } = await renderRoute('/books?offset=40')

    await wrapper.get('input[type="text"]').setValue(FOWLER)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({ author: FOWLER })
  })

  it('loads the filter and page from the URL', async () => {
    const requests = stubFetch(listHandler(makeCatalog(30, () => true)))
    const { wrapper } = await renderRoute('/books?author=Martin%20Fowler&offset=20')

    expect(requests).toHaveLength(1)
    expect(nth(requests, 0).url.searchParams.get('author')).toBe(FOWLER)
    expect(nth(requests, 0).url.searchParams.get('offset')).toBe('20')
    expect(authorInput(wrapper).value).toBe(FOWLER)
    expect(wrapper.text()).toContain('21–30 of 30')
  })

  it('uses the first of repeated authors, trimmed', async () => {
    const requests = stubFetch(listHandler(makeCatalog(25)))
    await renderRoute('/books?offset=-5&author=%20Martin%20Fowler%20&author=x')
    expect(nth(requests, 0).url.searchParams.get('offset')).toBe('0')
    expect(nth(requests, 0).url.searchParams.get('author')).toBe(FOWLER)
  })

  it('treats a bad offset as 0', async () => {
    const requests = stubFetch(listHandler(makeCatalog(25)))
    await renderRoute('/books?offset=abc')
    expect(nth(requests, 0).url.searchParams.get('offset')).toBe('0')
    expect(nth(requests, 0).url.searchParams.has('author')).toBe(false)
  })

  it('follows the URL on back and forward', async () => {
    stubFetch(listHandler(makeCatalog(25)))
    const { wrapper, router } = await renderRoute(`/books?author=${FOWLER}`)
    expect(authorInput(wrapper).value).toBe(FOWLER)

    await router.push('/books')
    await flushPromises()
    expect(authorInput(wrapper).value).toBe('')
    expect(wrapper.text()).toContain('1–20 of 25')

    router.back()
    await flushPromises()
    expect(authorInput(wrapper).value).toBe(FOWLER)
    expect(wrapper.text()).toContain('1–5 of 5')
  })

  it('shows an empty catalog with a New book link', async () => {
    stubFetch(listHandler([]))
    const { wrapper } = await renderRoute('/books')

    expect(wrapper.text()).toContain('No books yet')
    const link = wrapper.findAll('a').find((a) => a.text() === 'New book')
    expect(link?.attributes('href')).toBe('/books/new')
  })

  it('shows a message when a filter has no matches', async () => {
    stubFetch(listHandler(makeCatalog(25, () => false)))
    const { wrapper, router } = await renderRoute(`/books?author=${FOWLER}`)

    expect(wrapper.text()).toContain('No books by Martin Fowler')
    expect(wrapper.text()).not.toContain('No books yet')
    expect(wrapper.findAll('a').some((a) => a.text() === 'New book')).toBe(true)

    const clear = wrapper.findAll('button').filter((b) => b.text() === 'Clear filter')
    await nth(clear, -1).trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/books')
    expect(wrapper.text()).toContain('1–20 of 25')
  })

  it('shows a page past the end with a link to the first page', async () => {
    stubFetch(listHandler(makeCatalog(25)))
    const { wrapper } = await renderRoute('/books?offset=100')

    expect(wrapper.text()).toContain('No books on this page')
    const link = wrapper.findAll('a').find((a) => a.text() === 'Go to the first page')
    expect(link?.attributes('href')).toBe('/books')
  })

  it('links each title to its details and renders missing fields as a dash', async () => {
    stubFetch(
      listHandler([
        makeBook({ id: 3, title: 'A', authors: ['X', 'Y'], publishedYear: 2008, language: 'en' }),
        makeBook({ id: 4, title: 'B' }),
      ]),
    )
    const { wrapper } = await renderRoute('/books')

    const links = wrapper.findAll('tbody a')
    expect(links.map((a) => a.attributes('href'))).toEqual(['/books/3', '/books/4'])
    const cells = wrapper.findAll('tbody tr').map((r) => r.findAll('td').map((c) => c.text()))
    expect(cells).toEqual([
      ['A', 'X, Y', '2008', 'en'],
      ['B', 'Robert C. Martin', '—', '—'],
    ])
  })

  it('renders book data as text, not markup', async () => {
    stubFetch(listHandler([makeBook({ title: '<b>bold</b>' })]))
    const { wrapper } = await renderRoute('/books')
    expect(wrapper.find('tbody b').exists()).toBe(false)
    expect(wrapper.get('tbody a').text()).toBe('<b>bold</b>')
  })

  it('labels the author input', async () => {
    stubFetch(listHandler([]))
    const { wrapper } = await renderRoute('/books')
    const label = wrapper.get('label')
    expect(label.text()).toBe('Author')
    expect(label.attributes('for')).toBe(wrapper.get('input[type="text"]').attributes('id'))
  })

  describe('failures', () => {
    it.each([
      ['a network error', failWithNetworkError],
      ['a 500', () => errorResponse(500, 'internal error')],
    ])('shows %s and retries', async (_name, fail) => {
      let failing = true
      const ok = listHandler(makeCatalog(25))
      const requests = stubFetch((req) => (failing ? fail() : ok(req)))
      const { wrapper } = await renderRoute('/books')

      expect(wrapper.get('[role="alert"]').text()).toContain('Could not load the books:')
      expect(wrapper.findAll('a').some((a) => a.text() === 'New book')).toBe(true)

      failing = false
      await button(wrapper, 'Retry').trigger('click')
      await flushPromises()

      expect(requests).toHaveLength(2)
      expect(nth(requests, 1).url.search).toBe(nth(requests, 0).url.search)
      expect(wrapper.find('[role="alert"]').exists()).toBe(false)
      expect(wrapper.text()).toContain('1–20 of 25')
    })

    it('shows a 400 from the API as an error', async () => {
      stubFetch(() => errorResponse(400, 'invalid query parameter "author"'))
      const { wrapper } = await renderRoute('/books?author=x')
      expect(wrapper.get('[role="alert"]').text()).toContain(
        'Could not load the books: invalid query parameter "author"',
      )
    })
  })

  describe('loading', () => {
    it('shows a status and disables Next and Filter while a request is pending', async () => {
      const respond = listHandler(makeCatalog(25))
      let release: () => void = () => {}
      let hold = false
      stubFetch(async (req) => {
        if (hold) await new Promise<void>((resolve) => (release = resolve))
        return respond(req)
      })
      const { wrapper } = await renderRoute('/books')
      expect(wrapper.find('[role="status"]').exists()).toBe(false)

      hold = true
      await button(wrapper, 'Next').trigger('click')
      await flushPromises()

      expect(wrapper.get('[role="status"]').text()).toBe('Loading books…')
      expect(button(wrapper, 'Next').attributes('disabled')).toBeDefined()
      expect(button(wrapper, 'Previous').attributes('disabled')).toBeDefined()
      expect(button(wrapper, 'Filter').attributes('disabled')).toBeDefined()

      release()
      await flushPromises()
      expect(wrapper.find('[role="status"]').exists()).toBe(false)
      expect(wrapper.text()).toContain('21–25 of 25')
    })

    it('ignores a response for an outdated query', async () => {
      const respond = listHandler(makeCatalog(25))
      const releases: Array<() => void> = []
      stubFetch(async (req) => {
        if (req.url.searchParams.has('author')) {
          await new Promise<void>((resolve) => releases.push(resolve))
        }
        return respond(req)
      })
      const { wrapper, router } = await renderRoute('/books')

      await router.push(`/books?author=${FOWLER}`)
      await router.push('/books?offset=20')
      await flushPromises()
      releases.forEach((r) => r())
      await flushPromises()

      expect(wrapper.text()).toContain('21–25 of 25')
      expect(wrapper.text()).not.toContain('1–5 of 5')
    })
  })
})
