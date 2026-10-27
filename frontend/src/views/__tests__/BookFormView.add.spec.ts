import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, type VueWrapper } from '@vue/test-utils'

import { errorResponse, jsonResponse, makeBook, renderRoute, stubFetch } from '@/__tests__/helpers'

afterEach(() => {
  vi.unstubAllGlobals()
})

async function openForm() {
  const rendered = await renderRoute('/books/new')
  return rendered
}

async function fill(wrapper: VueWrapper, values: Record<string, string>) {
  for (const [id, value] of Object.entries(values)) {
    await wrapper.get(`#${id}`).setValue(value)
  }
}

async function submit(wrapper: VueWrapper) {
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

function stubCreate(response: () => Response) {
  return stubFetch(({ method, url }) => {
    if (method === 'POST' && url.pathname === '/api/books') return response()
    if (method === 'GET' && url.pathname === '/api/books/42') {
      return jsonResponse(200, makeBook({ id: 42 }))
    }
    return undefined
  })
}

const valid = {
  'book-isbn': '978-0-13-235088-4',
  'book-title': 'Clean Code',
  'book-author-1': 'Robert C. Martin',
}

describe('BookFormView in add mode', () => {
  it('shows an empty form with one author input', async () => {
    stubFetch(() => undefined)
    const { wrapper } = await openForm()

    expect(wrapper.get('h1').text()).toBe('New book')
    expect(wrapper.get('button[type="submit"]').text()).toBe('Add book')
    expect(wrapper.get<HTMLInputElement>('#book-isbn').element.value).toBe('')
    expect(wrapper.findAll('fieldset input')).toHaveLength(1)
    expect(wrapper.get('form').attributes('novalidate')).toBeDefined()
  })

  it('labels every input and marks the required ones', async () => {
    stubFetch(() => undefined)
    const { wrapper } = await openForm()

    const labelled: Record<string, string> = {
      'book-isbn': 'ISBN',
      'book-title': 'Title',
      'book-author-1': 'Author 1',
      'book-publishedYear': 'Published year',
      'book-language': 'Language',
      'book-pages': 'Pages',
    }
    for (const [id, text] of Object.entries(labelled)) {
      expect(wrapper.get(`label[for="${id}"]`).text()).toContain(text)
    }
    expect(wrapper.get('legend').text()).toContain('Authors')
    for (const id of ['book-isbn', 'book-title']) {
      expect(wrapper.get(`#${id}`).attributes('aria-required')).toBe('true')
    }
    expect(wrapper.get('fieldset').attributes('aria-required')).toBe('true')
    expect(wrapper.get('#book-publishedYear').attributes('type')).toBe('number')
    expect(wrapper.get('#book-pages').attributes('type')).toBe('number')
    for (const attr of ['required', 'min', 'max', 'maxlength', 'pattern']) {
      expect(wrapper.find(`[${attr}]`).exists()).toBe(false)
    }
  })

  it('links Cancel to the book list', async () => {
    stubFetch(() => undefined)
    const { wrapper } = await openForm()

    const cancel = wrapper.findAll('a').find((a) => a.text() === 'Cancel')
    expect(cancel?.attributes('href')).toBe('/books')
  })

  it('creates the book without empty optional fields and shows its details', async () => {
    const requests = stubCreate(() => jsonResponse(201, makeBook({ id: 42 })))
    const { wrapper, router } = await openForm()

    await fill(wrapper, valid)
    await wrapper.get('fieldset > button').trigger('click')
    await wrapper.get('#book-author-2').setValue('Someone Else')
    await submit(wrapper)

    const post = requests.filter((r) => r.method === 'POST')
    expect(post).toHaveLength(1)
    expect(post[0]?.body).toEqual({
      isbn: '978-0-13-235088-4',
      title: 'Clean Code',
      authors: ['Robert C. Martin', 'Someone Else'],
    })
    await vi.waitFor(() => expect(router.currentRoute.value.fullPath).toBe('/books/42'))
  })

  it('sends the optional fields as numbers and text when filled', async () => {
    const requests = stubCreate(() => jsonResponse(201, makeBook({ id: 42 })))
    const { wrapper } = await openForm()

    await fill(wrapper, {
      ...valid,
      'book-publishedYear': '2008',
      'book-language': 'en',
      'book-pages': '464',
    })
    await submit(wrapper)

    expect(requests.find((r) => r.method === 'POST')?.body).toEqual({
      isbn: '978-0-13-235088-4',
      title: 'Clean Code',
      authors: ['Robert C. Martin'],
      publishedYear: 2008,
      language: 'en',
      pages: 464,
    })
  })

  it('treats a whitespace-only language as empty', async () => {
    const requests = stubCreate(() => jsonResponse(201, makeBook({ id: 42 })))
    const { wrapper } = await openForm()

    await fill(wrapper, { ...valid, 'book-language': '   ' })
    await submit(wrapper)

    expect(requests.find((r) => r.method === 'POST')?.body).toEqual({
      isbn: '978-0-13-235088-4',
      title: 'Clean Code',
      authors: ['Robert C. Martin'],
    })
  })

  it('sends an empty author input too', async () => {
    const requests = stubCreate(() => errorResponse(400, 'authors: each author name must have 1 to 200 characters'))
    const { wrapper } = await openForm()

    await fill(wrapper, valid)
    await wrapper.get('fieldset > button').trigger('click')
    await submit(wrapper)

    expect(requests.find((r) => r.method === 'POST')?.body).toMatchObject({
      authors: ['Robert C. Martin', ''],
    })
  })

  it('shows a 400 at the title field and keeps the input', async () => {
    stubCreate(() => errorResponse(400, 'title: must have 1 to 300 characters'))
    const { wrapper, router } = await openForm()

    await fill(wrapper, valid)
    await submit(wrapper)

    const error = wrapper.get('#book-title-error')
    expect(error.text()).toBe('must have 1 to 300 characters')
    const title = wrapper.get('#book-title')
    expect(title.attributes('aria-invalid')).toBe('true')
    expect(title.attributes('aria-describedby')).toBe('book-title-error')
    expect(wrapper.get<HTMLInputElement>('#book-isbn').element.value).toBe('978-0-13-235088-4')
    expect(wrapper.get<HTMLInputElement>('#book-title').element.value).toBe('Clean Code')
    expect(wrapper.get<HTMLInputElement>('#book-author-1').element.value).toBe('Robert C. Martin')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(router.currentRoute.value.fullPath).toBe('/books/new')
  })

  it('shows a 400 on an optional field at that field', async () => {
    stubCreate(() => errorResponse(400, 'pages: must be between 1 and 10000'))
    const { wrapper } = await openForm()

    await fill(wrapper, { ...valid, 'book-pages': '0' })
    await submit(wrapper)

    expect(wrapper.get('#book-pages-error').text()).toBe('must be between 1 and 10000')
    expect(wrapper.get('#book-pages').attributes('aria-describedby')).toBe('book-pages-error')
    expect(wrapper.get('#book-pages').attributes('aria-invalid')).toBe('true')
    expect(wrapper.get('#book-title').attributes('aria-invalid')).toBeUndefined()
  })

  it('shows a 400 on authors in the fieldset, for the fieldset and every input', async () => {
    stubCreate(() => errorResponse(400, 'authors: each author name must have 1 to 200 characters'))
    const { wrapper } = await openForm()

    await fill(wrapper, valid)
    await wrapper.get('fieldset > button').trigger('click')
    await submit(wrapper)

    expect(wrapper.get('fieldset #book-authors-error').text()).toBe(
      'each author name must have 1 to 200 characters',
    )
    const fieldset = wrapper.get('fieldset')
    expect(fieldset.attributes('aria-invalid')).toBe('true')
    expect(fieldset.attributes('aria-describedby')).toBe('book-authors-error')
    for (const id of ['book-author-1', 'book-author-2']) {
      expect(wrapper.get(`#${id}`).attributes('aria-invalid')).toBe('true')
      expect(wrapper.get(`#${id}`).attributes('aria-describedby')).toBe('book-authors-error')
    }
  })

  it('shows a 400 that names no form field at the top', async () => {
    stubCreate(() => errorResponse(400, 'invalid request body'))
    const { wrapper } = await openForm()

    await fill(wrapper, valid)
    await submit(wrapper)

    expect(wrapper.get('[role="alert"]').text()).toContain('invalid request body')
    expect(wrapper.find('.field-error').exists()).toBe(false)
    expect(wrapper.get<HTMLInputElement>('#book-title').element.value).toBe('Clean Code')
  })

  it('shows a duplicate ISBN at the ISBN field', async () => {
    stubCreate(() => errorResponse(409, 'a book with this isbn already exists'))
    const { wrapper } = await openForm()

    await fill(wrapper, valid)
    await submit(wrapper)

    expect(wrapper.get('#book-isbn-error').text()).toBe('A book with this ISBN already exists')
    expect(wrapper.get('#book-isbn').attributes('aria-describedby')).toBe('book-isbn-error')
    expect(wrapper.get('#book-isbn').attributes('aria-invalid')).toBe('true')
  })

  it('shows a network error at the top and keeps the input', async () => {
    stubFetch(() => {
      throw new TypeError('Failed to fetch')
    })
    const { wrapper } = await openForm()

    await fill(wrapper, valid)
    await submit(wrapper)

    expect(wrapper.get('[role="alert"]').text()).toContain('Could not reach the server.')
    expect(wrapper.get<HTMLInputElement>('#book-title').element.value).toBe('Clean Code')
  })

  it('shows a 500 at the top and keeps the input', async () => {
    stubCreate(() => errorResponse(500, 'internal error'))
    const { wrapper } = await openForm()

    await fill(wrapper, valid)
    await submit(wrapper)

    expect(wrapper.get('[role="alert"]').text()).toContain('internal error')
    expect(wrapper.get<HTMLInputElement>('#book-title').element.value).toBe('Clean Code')
  })

  it('clears the previous errors on the next submit', async () => {
    let first = true
    stubCreate(() => {
      if (first) {
        first = false
        return errorResponse(400, 'title: must have 1 to 300 characters')
      }
      return errorResponse(500, 'internal error')
    })
    const { wrapper } = await openForm()

    await fill(wrapper, valid)
    await submit(wrapper)
    expect(wrapper.find('#book-title-error').exists()).toBe(true)

    await submit(wrapper)
    expect(wrapper.find('#book-title-error').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('internal error')
  })

  it('sends one request for two quick submits and disables the button meanwhile', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const requests = stubFetch(async ({ method, url }) => {
      if (method === 'POST') {
        await gate
        return jsonResponse(201, makeBook({ id: 42 }))
      }
      if (url.pathname === '/api/books/42') return jsonResponse(200, makeBook({ id: 42 }))
      return undefined
    })
    const { wrapper } = await openForm()

    await fill(wrapper, valid)
    const form = wrapper.get('form')
    await form.trigger('submit')
    await form.trigger('submit')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()

    release()
    await flushPromises()
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1)
  })

  describe('authors list', () => {
    it('adds and removes inputs, renumbering them', async () => {
      stubFetch(() => undefined)
      const { wrapper } = await openForm()

      await wrapper.get('fieldset > button').trigger('click')
      await wrapper.get('fieldset > button').trigger('click')
      expect(wrapper.findAll('fieldset input')).toHaveLength(3)

      await wrapper.get('#book-author-1').setValue('A')
      await wrapper.get('#book-author-2').setValue('B')
      await wrapper.get('#book-author-3').setValue('C')
      await wrapper.get('button[aria-label="Remove author 2"]').trigger('click')

      const values = wrapper
        .findAll<HTMLInputElement>('fieldset input')
        .map((input) => input.element.value)
      expect(values).toEqual(['A', 'C'])
      expect(wrapper.get('label[for="book-author-2"]').text()).toBe('Author 2')
    })

    it('does not allow removing the last input', async () => {
      stubFetch(() => undefined)
      const { wrapper } = await openForm()

      expect(wrapper.get('button[aria-label="Remove author 1"]').attributes('disabled')).toBeDefined()
      await wrapper.get('fieldset > button').trigger('click')
      expect(wrapper.get('button[aria-label="Remove author 1"]').attributes('disabled')).toBeUndefined()
      await wrapper.get('button[aria-label="Remove author 1"]').trigger('click')
      expect(wrapper.findAll('fieldset input')).toHaveLength(1)
      expect(wrapper.get('button[aria-label="Remove author 1"]').attributes('disabled')).toBeDefined()
    })

    it('disables Add author at 20 inputs', async () => {
      stubFetch(() => undefined)
      const { wrapper } = await openForm()

      for (let i = 0; i < 19; i++) {
        await wrapper.get('fieldset > button').trigger('click')
      }
      expect(wrapper.findAll('fieldset input')).toHaveLength(20)
      expect(wrapper.get('fieldset > button').attributes('disabled')).toBeDefined()

      await wrapper.get('button[aria-label="Remove author 20"]').trigger('click')
      expect(wrapper.get('fieldset > button').attributes('disabled')).toBeUndefined()
    })
  })
})
