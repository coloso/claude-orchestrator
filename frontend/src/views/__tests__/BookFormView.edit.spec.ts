import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, type VueWrapper } from '@vue/test-utils'

import {
  errorResponse,
  jsonResponse,
  makeBook,
  renderRoute,
  stubFetch,
  type RecordedRequest,
} from '@/__tests__/helpers'

afterEach(() => {
  vi.unstubAllGlobals()
})

const full = {
  id: 7,
  isbn: '9780132350884',
  title: 'Clean Code',
  authors: ['Robert C. Martin', 'Dean Wampler'],
  publishedYear: 2008,
  language: 'en',
  pages: 464,
  version: 3,
}

function value(wrapper: VueWrapper, id: string): string {
  return wrapper.get<HTMLInputElement>(`#${id}`).element.value
}

async function submit(wrapper: VueWrapper) {
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

type PutHandler = (req: RecordedRequest) => Response | Promise<Response>

/** Serves GET /api/books/7 from `current`; PUT is answered by `put`. */
function stubBook(put: PutHandler, current: () => Response) {
  return stubFetch((req) => {
    if (req.url.pathname !== '/api/books/7') return undefined
    if (req.method === 'GET') return current()
    if (req.method === 'PUT') return put(req)
    return undefined
  })
}

async function openEdit(
  put: PutHandler = () => jsonResponse(200, makeBook({ ...full, version: 4 })),
  current: () => Response = () => jsonResponse(200, makeBook(full)),
) {
  const requests = stubBook(put, current)
  const rendered = await renderRoute('/books/7/edit')
  return { ...rendered, requests }
}

describe('BookFormView in edit mode', () => {
  it('fills the form from the loaded book', async () => {
    const { wrapper } = await openEdit()

    expect(wrapper.get('h1').text()).toBe('Edit book')
    expect(wrapper.get('button[type="submit"]').text()).toBe('Save')
    expect(value(wrapper, 'book-isbn')).toBe('9780132350884')
    expect(value(wrapper, 'book-title')).toBe('Clean Code')
    expect(value(wrapper, 'book-author-1')).toBe('Robert C. Martin')
    expect(value(wrapper, 'book-author-2')).toBe('Dean Wampler')
    expect(value(wrapper, 'book-publishedYear')).toBe('2008')
    expect(value(wrapper, 'book-language')).toBe('en')
    expect(value(wrapper, 'book-pages')).toBe('464')
  })

  it('links Cancel to the details page', async () => {
    const { wrapper } = await openEdit()

    const cancel = wrapper.findAll('a').find((a) => a.text() === 'Cancel')
    expect(cancel?.attributes('href')).toBe('/books/7')
  })

  it('shows a loading state while the book loads', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    stubFetch(async () => {
      await gate
      return jsonResponse(200, makeBook(full))
    })
    const { wrapper } = await renderRoute('/books/7/edit')

    expect(wrapper.get('[role="status"]').text()).toContain('Loading')
    expect(wrapper.find('form').exists()).toBe(false)

    release()
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(true)
  })

  it('saves all editable fields with the loaded version and shows the details', async () => {
    const { wrapper, router, requests } = await openEdit()

    await wrapper.get('#book-title').setValue('Clean Code, 2nd edition')
    await submit(wrapper)

    const put = requests.filter((r) => r.method === 'PUT')
    expect(put).toHaveLength(1)
    expect(put[0]?.body).toEqual({
      isbn: '9780132350884',
      title: 'Clean Code, 2nd edition',
      authors: ['Robert C. Martin', 'Dean Wampler'],
      publishedYear: 2008,
      language: 'en',
      pages: 464,
      version: 3,
    })
    await vi.waitFor(() => expect(router.currentRoute.value.fullPath).toBe('/books/7'))
  })

  it('leaves out an optional field that was cleared', async () => {
    const { wrapper, requests } = await openEdit()

    await wrapper.get('#book-publishedYear').setValue('')
    await wrapper.get('#book-language').setValue('')
    await wrapper.get('#book-pages').setValue('')
    await submit(wrapper)

    const body = requests.find((r) => r.method === 'PUT')?.body
    expect(body).toEqual({
      isbn: '9780132350884',
      title: 'Clean Code',
      authors: ['Robert C. Martin', 'Dean Wampler'],
      version: 3,
    })
  })

  it('sends one request for two quick submits', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const { wrapper, requests } = await openEdit(async () => {
      await gate
      return jsonResponse(200, makeBook({ ...full, version: 4 }))
    })

    const form = wrapper.get('form')
    await form.trigger('submit')
    await form.trigger('submit')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    release()
    await flushPromises()

    expect(requests.filter((r) => r.method === 'PUT')).toHaveLength(1)
  })

  describe('version conflict', () => {
    const conflict = () => errorResponse(409, 'version conflict: the book was changed')

    it('shows the conflict, keeps the input and does not resubmit', async () => {
      const { wrapper, requests } = await openEdit(conflict)

      await wrapper.get('#book-title').setValue('My title')
      await submit(wrapper)

      const alert = wrapper.get('[role="alert"]')
      expect(alert.text()).toContain('This book was changed by someone else.')
      expect(alert.text()).toContain('Load latest')
      expect(value(wrapper, 'book-title')).toBe('My title')
      expect(wrapper.find('#book-isbn-error').exists()).toBe(false)
      await flushPromises()
      expect(requests.filter((r) => r.method === 'PUT')).toHaveLength(1)
      expect(requests.filter((r) => r.method === 'GET')).toHaveLength(1)
    })

    it('reloads the form on Load latest and then saves the new version', async () => {
      let served = makeBook(full)
      const { wrapper, router, requests } = await openEdit(
        (req) => {
          const body = req.body as { version: number }
          if (body.version === 3) return conflict()
          return jsonResponse(200, makeBook({ ...served, version: body.version + 1 }))
        },
        () => jsonResponse(200, served),
      )

      await wrapper.get('#book-title').setValue('My title')
      await submit(wrapper)
      expect(wrapper.find('[role="alert"]').exists()).toBe(true)

      served = makeBook({ ...full, title: 'Theirs', pages: 500, version: 5 })
      const load = wrapper.findAll('button').find((b) => b.text() === 'Load latest')
      await load?.trigger('click')
      await flushPromises()

      expect(value(wrapper, 'book-title')).toBe('Theirs')
      expect(value(wrapper, 'book-pages')).toBe('500')
      expect(wrapper.find('[role="alert"]').exists()).toBe(false)

      await wrapper.get('#book-title').setValue('Theirs, revised')
      await submit(wrapper)

      const put = requests.filter((r) => r.method === 'PUT')
      expect(put).toHaveLength(2)
      expect(put[1]?.body).toMatchObject({ title: 'Theirs, revised', version: 5 })
      await vi.waitFor(() => expect(router.currentRoute.value.fullPath).toBe('/books/7'))
    })

    it('keeps the input if Load latest fails', async () => {
      let fail = false
      const { wrapper } = await openEdit(conflict, () =>
        fail ? errorResponse(500, 'internal error') : jsonResponse(200, makeBook(full)),
      )

      await wrapper.get('#book-title').setValue('My title')
      await submit(wrapper)
      fail = true
      const load = wrapper.findAll('button').find((b) => b.text() === 'Load latest')
      await load?.trigger('click')
      await flushPromises()

      expect(value(wrapper, 'book-title')).toBe('My title')
      expect(wrapper.text()).toContain('internal error')
      expect(wrapper.text()).toContain('This book was changed by someone else.')
    })
  })

  it('shows a duplicate ISBN at the ISBN field', async () => {
    const { wrapper } = await openEdit(() => errorResponse(409, 'a book with this isbn already exists'))

    await submit(wrapper)

    expect(wrapper.get('#book-isbn-error').text()).toBe('A book with this ISBN already exists')
    expect(wrapper.get('#book-isbn').attributes('aria-describedby')).toBe('book-isbn-error')
    expect(wrapper.get('#book-isbn').attributes('aria-invalid')).toBe('true')
  })

  it('shows a 400 at its field', async () => {
    const { wrapper } = await openEdit(() => errorResponse(400, 'pages: must be between 1 and 10000'))

    await wrapper.get('#book-pages').setValue('0')
    await submit(wrapper)

    expect(wrapper.get('#book-pages-error').text()).toBe('must be between 1 and 10000')
    expect(value(wrapper, 'book-pages')).toBe('0')
  })

  it('shows "This book no longer exists." when the save answers 404', async () => {
    const { wrapper } = await openEdit(() => errorResponse(404, 'book not found'))

    await wrapper.get('#book-title').setValue('My title')
    await submit(wrapper)

    const alert = wrapper.get('[role="alert"]')
    expect(alert.text()).toContain('This book no longer exists.')
    expect(alert.get('a').attributes('href')).toBe('/books')
    expect(value(wrapper, 'book-title')).toBe('My title')
  })

  it('shows a 500 at the top and keeps the input', async () => {
    const { wrapper } = await openEdit(() => errorResponse(500, 'internal error'))

    await wrapper.get('#book-title').setValue('My title')
    await submit(wrapper)

    expect(wrapper.get('[role="alert"]').text()).toContain('internal error')
    expect(value(wrapper, 'book-title')).toBe('My title')
  })

  it('shows a network error at the top and keeps the input', async () => {
    let offline = false
    const { wrapper } = await openEdit(() => {
      offline = true
      throw new TypeError('Failed to fetch')
    })

    await wrapper.get('#book-title').setValue('My title')
    await submit(wrapper)

    expect(offline).toBe(true)
    expect(wrapper.get('[role="alert"]').text()).toContain('Could not reach the server.')
    expect(value(wrapper, 'book-title')).toBe('My title')
  })

  describe('loading errors', () => {
    it('shows "Book not found" without a form for an unknown book', async () => {
      stubFetch(() => errorResponse(404, 'book not found'))
      const { wrapper } = await renderRoute('/books/999/edit')

      expect(wrapper.get('h1').text()).toBe('Book not found')
      expect(wrapper.find('form').exists()).toBe(false)
      expect(wrapper.findAll('a').some((a) => a.attributes('href') === '/books' && a.text() !== 'Books')).toBe(
        true,
      )
    })

    it('shows other load errors with a Retry button', async () => {
      let fail = true
      stubFetch(() => (fail ? errorResponse(500, 'internal error') : jsonResponse(200, makeBook(full))))
      const { wrapper } = await renderRoute('/books/7/edit')

      expect(wrapper.get('[role="alert"]').text()).toContain('internal error')
      expect(wrapper.find('form').exists()).toBe(false)

      fail = false
      await wrapper.get('[role="alert"] button').trigger('click')
      await flushPromises()

      expect(wrapper.find('[role="alert"]').exists()).toBe(false)
      expect(value(wrapper, 'book-title')).toBe('Clean Code')
    })

    it('shows a network error with a Retry button', async () => {
      stubFetch(() => {
        throw new TypeError('Failed to fetch')
      })
      const { wrapper } = await renderRoute('/books/7/edit')

      const alert = wrapper.get('[role="alert"]')
      expect(alert.text()).toContain('Could not reach the server.')
      expect(alert.get('button').text()).toBe('Retry')
    })
  })
})
