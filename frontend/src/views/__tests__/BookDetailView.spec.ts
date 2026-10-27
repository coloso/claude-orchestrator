import { describe, expect, it, vi } from 'vitest'
import { flushPromises, type VueWrapper } from '@vue/test-utils'

import {
  errorResponse,
  jsonResponse,
  makeBook,
  noContent,
  renderRoute,
  stubFetch,
  type Book,
  type RecordedRequest,
} from '@/__tests__/helpers'

function backend(opts: { book?: Book; deleteResponse?: () => Response | Promise<Response> } = {}) {
  return stubFetch((req) => {
    const { pathname } = req.url
    if (pathname === '/api/books' && req.method === 'GET') {
      return jsonResponse(200, { items: [], total: 0 })
    }
    if (pathname === '/api/books/7' && req.method === 'GET') {
      return opts.book ? jsonResponse(200, opts.book) : errorResponse(404, 'book not found')
    }
    if (pathname === '/api/books/7' && req.method === 'DELETE') {
      return (opts.deleteResponse ?? noContent)()
    }
    return undefined
  })
}

const deletes = (reqs: RecordedRequest[]) => reqs.filter((r) => r.method === 'DELETE')

function buttons(wrapper: VueWrapper, name: string) {
  return wrapper.findAll('button').filter((b) => b.text() === name)
}

async function openConfirm(wrapper: VueWrapper) {
  await buttons(wrapper, 'Delete')[0]!.trigger('click')
  await flushPromises()
}

describe('BookDetailView', () => {
  it('shows all fields of a fully populated book', async () => {
    backend({
      book: makeBook({
        publishedYear: 2008,
        language: 'en',
        pages: 464,
        authors: ['Robert C. Martin', 'Someone Else'],
        version: 3,
      }),
    })
    const { wrapper } = await renderRoute('/books/7')
    expect(wrapper.find('h1').text()).toBe('Clean Code')
    const text = wrapper.find('dl').text()
    for (const part of ['9780132350884', 'Robert C. Martin', 'Someone Else', '2008', 'en', '464', '3']) {
      expect(text).toContain(part)
    }
    expect(text).toContain(new Date('2026-01-02T03:04:05Z').toLocaleString())
    expect(text).not.toContain('—')
    expect(wrapper.findAll('dl li')).toHaveLength(2)
  })

  it('shows a dash for unset optional fields', async () => {
    backend({ book: makeBook() })
    const { wrapper } = await renderRoute('/books/7')
    const dds = wrapper.findAll('dd').map((d) => d.text())
    expect(dds.filter((t) => t === '—')).toHaveLength(3)
  })

  it('shows a loading state', async () => {
    stubFetch(() => new Promise<Response>(() => {}))
    const { wrapper } = await renderRoute('/books/7')
    expect(wrapper.find('[role="status"]').text()).toBe('Loading book…')
  })

  it('shows not found with a link to the catalog', async () => {
    backend()
    const { wrapper } = await renderRoute('/books/7')
    expect(wrapper.find('h1').text()).toBe('Book not found')
    expect(wrapper.find('main a[href="/books"]').text()).toBe('Back to the catalog')
  })

  it('shows a load error and retries', async () => {
    let calls = 0
    stubFetch((req) => {
      if (req.url.pathname === '/api/books/7') {
        calls++
        return calls === 1 ? errorResponse(500, 'boom') : jsonResponse(200, makeBook())
      }
      return undefined
    })
    const { wrapper } = await renderRoute('/books/7')
    expect(wrapper.find('[role="alert"]').text()).toContain('Could not load the book: boom')
    await buttons(wrapper, 'Retry')[0]!.trigger('click')
    await vi.waitFor(() => expect(wrapper.find('h1').text()).toBe('Clean Code'))
  })

  it('links Edit to the edit route', async () => {
    backend({ book: makeBook() })
    const { wrapper } = await renderRoute('/books/7')
    const edit = wrapper.findAll('a').find((a) => a.text() === 'Edit')
    expect(edit?.attributes('href')).toBe('/books/7/edit')
  })

  it('renders markup in the title as text', async () => {
    backend({ book: makeBook({ title: '<img src=x onerror=alert(1)>' }) })
    const { wrapper } = await renderRoute('/books/7')
    expect(wrapper.find('h1').text()).toBe('<img src=x onerror=alert(1)>')
    expect(wrapper.find('img').exists()).toBe(false)
  })

  it('opens a confirmation naming the title, focusing Cancel, with one Delete button', async () => {
    backend({ book: makeBook() })
    const focus = vi.spyOn(HTMLElement.prototype, 'focus')
    const { wrapper } = await renderRoute('/books/7')
    await openConfirm(wrapper)
    const group = wrapper.find('[role="group"]')
    expect(group.text()).toContain('Delete "Clean Code"? This cannot be undone.')
    expect(buttons(wrapper, 'Delete')).toHaveLength(1)
    expect(focus.mock.contexts.some((el) => (el as HTMLElement).textContent?.trim() === 'Cancel')).toBe(true)
    focus.mockRestore()
  })

  it('Cancel closes the confirmation without a request', async () => {
    const reqs = backend({ book: makeBook() })
    const { wrapper } = await renderRoute('/books/7')
    await openConfirm(wrapper)
    await buttons(wrapper, 'Cancel')[0]!.trigger('click')
    expect(wrapper.find('[role="group"]').exists()).toBe(false)
    expect(deletes(reqs)).toHaveLength(0)
    expect(buttons(wrapper, 'Delete')).toHaveLength(1)
  })

  it('deletes, returns to the catalog and shows a notice', async () => {
    const reqs = backend({ book: makeBook() })
    const { wrapper, router } = await renderRoute('/books/7')
    await openConfirm(wrapper)
    await buttons(wrapper, 'Delete')[0]!.trigger('click')
    await vi.waitFor(() => expect(router.currentRoute.value.path).toBe('/books'))
    expect(deletes(reqs)).toHaveLength(1)
    await vi.waitFor(() => expect(wrapper.find('.notice').text()).toContain('Deleted Clean Code'))
  })

  it('treats 404 on delete as removed', async () => {
    backend({ book: makeBook(), deleteResponse: () => errorResponse(404, 'gone') })
    const { wrapper, router } = await renderRoute('/books/7')
    await openConfirm(wrapper)
    await buttons(wrapper, 'Delete')[0]!.trigger('click')
    await vi.waitFor(() => expect(router.currentRoute.value.path).toBe('/books'))
    await vi.waitFor(() => expect(wrapper.find('.notice').text()).toContain('Deleted Clean Code'))
  })

  const failures: [string, () => Response][] = [
    ['server error', () => errorResponse(500, 'db down')],
    [
      'network error',
      () => {
        throw new Error('offline')
      },
    ],
  ]
  it.each(failures)('stays on the page after a %s on delete', async (_name, deleteResponse) => {
    backend({ book: makeBook(), deleteResponse })
    const { wrapper, router } = await renderRoute('/books/7')
    await openConfirm(wrapper)
    await buttons(wrapper, 'Delete')[0]!.trigger('click')
    await vi.waitFor(() => expect(wrapper.find('[role="group"] [role="alert"]').exists()).toBe(true))
    expect(router.currentRoute.value.path).toBe('/books/7')
    expect(buttons(wrapper, 'Delete')[0]!.attributes('disabled')).toBeUndefined()
    expect(buttons(wrapper, 'Cancel')[0]!.attributes('disabled')).toBeUndefined()
  })

  it('sends one DELETE on a double click', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => {
      release = r
    })
    const reqs = backend({
      book: makeBook(),
      deleteResponse: async () => {
        await gate
        return noContent()
      },
    })
    const { wrapper } = await renderRoute('/books/7')
    await openConfirm(wrapper)
    const del = buttons(wrapper, 'Delete')[0]!
    await del.trigger('click')
    await del.trigger('click')
    await vi.waitFor(() => expect(deletes(reqs)).toHaveLength(1))
    expect(buttons(wrapper, 'Cancel')[0]!.attributes('disabled')).toBeDefined()
    release()
    await flushPromises()
    expect(deletes(reqs)).toHaveLength(1)
  })
})
