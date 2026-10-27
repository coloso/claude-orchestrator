// Security regression tests (S04-08): book data, API error messages and the notice are shown as
// text only. Every payload below would create an element or an attribute if it were interpreted.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, type VueWrapper } from '@vue/test-utils'

import { errorResponse, jsonResponse, makeBook, noContent, renderRoute, stubFetch } from '@/__tests__/helpers'

afterEach(() => {
  vi.unstubAllGlobals()
})

const IMG = '<img src=x onerror=alert(1)>'
const SCRIPT = '<script>alert(1)</script>'
const SVG = '<svg onload=alert(1)>'
const BREAKOUT = '"><iframe src=javascript:alert(1)></iframe>'
const JS_URL = 'javascript:alert(1)'
const TEMPLATE = '{{ 7 * 7 }}'

const PAYLOADS = [IMG, SCRIPT, SVG, BREAKOUT, JS_URL, TEMPLATE]

// Every tag the UI itself renders. Anything else in the output was injected.
const APP_TAGS = new Set([
  'a', 'article', 'bdi', 'body', 'button', 'dd', 'div', 'dl', 'dt', 'fieldset', 'form', 'h1', 'head',
  'header', 'html', 'input', 'label', 'legend', 'li', 'main', 'nav', 'p', 'section', 'span',
  'strong', 'table', 'tbody', 'td', 'th', 'thead', 'tr', 'ul',
])
const APP_HREF = /^\/books(\/new|\/[1-9][0-9]{0,14}(\/edit)?)?(\?[A-Za-z0-9%=&_.~+*-]*)?$/
const FORBIDDEN_ATTRIBUTES = ['src', 'srcdoc', 'style', 'formaction', 'action', 'data', 'srcset', 'poster']

/** Parses the serialized page again: injected markup would show up as elements. */
function findInjection(wrapper: VueWrapper): string[] {
  const doc = new DOMParser().parseFromString(wrapper.html(), 'text/html')
  const found: string[] = []
  for (const el of Array.from(doc.querySelectorAll('*'))) {
    const tag = el.tagName.toLowerCase()
    if (!APP_TAGS.has(tag)) found.push(`<${tag}>`)
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase()
      if (name.startsWith('on') || FORBIDDEN_ATTRIBUTES.includes(name)) found.push(`${tag}[${name}]`)
      if (name === 'href' && !APP_HREF.test(attr.value)) found.push(`${tag}[href=${attr.value}]`)
      // The value of an input is data, not a URL; every other attribute must not carry a script URL.
      if (name !== 'value' && attr.value.toLowerCase().includes('javascript:')) {
        found.push(`${tag}[${name}] has javascript:`)
      }
    }
  }
  return found
}

function expectInert(wrapper: VueWrapper, shownAs: string): void {
  expect(findInjection(wrapper)).toEqual([])
  expect(wrapper.text()).toContain(shownAs)
}

function bookWith(payload: string) {
  return makeBook({
    id: 7,
    isbn: payload,
    title: payload,
    authors: [payload, `${payload} 2`],
    language: payload,
  })
}

describe('security: the page checker', () => {
  it('flags injected markup and attributes', async () => {
    stubFetch(() => undefined)
    const { wrapper } = await renderRoute('/books/new')
    expect(findInjection(wrapper)).toEqual([])
    const bad = document.createElement('div')
    bad.innerHTML = '<img src=x onerror=alert(1)><a href="javascript:alert(1)">x</a>'
    const fake = { html: () => bad.innerHTML } as unknown as VueWrapper
    expect(findInjection(fake).join()).toMatch(/<img>.*img\[src\].*img\[onerror\].*a\[href=javascript:alert\(1\)\]/)
  })
})

// The backend rejects control characters (category Cc) but accepts bidirectional formatting
// characters (Cf): a title may contain U+202E (right-to-left override) or U+2066..U+2069.
describe('security: bidirectional controls in book text', () => {
  const RLO = '‮evil'

  function stubDeletable() {
    stubFetch(({ method, url }) => {
      if (method === 'GET' && url.pathname === '/api/books/7') return jsonResponse(200, makeBook({ title: RLO }))
      if (method === 'DELETE' && url.pathname === '/api/books/7') return noContent()
      if (method === 'GET' && url.pathname === '/api/books') return jsonResponse(200, { items: [], total: 0 })
      return undefined
    })
  }

  it('still shows the text itself unchanged', async () => {
    stubDeletable()
    const { wrapper } = await renderRoute('/books/7')

    expect(wrapper.get('h1').text()).toBe(RLO)
  })

  it.skip('SECURITY SEC-S04-2: fixed by follow-up "Isolate book text from bidirectional controls"', async () => {
    // Text built around a title ("Delete "<title>"? ...", "Deleted <title>") must keep the title in
    // its own bidi isolate, so that an override in the title cannot reorder the words around it.
    stubDeletable()
    const { wrapper } = await renderRoute('/books/7')
    await wrapper.get('.actions button').trigger('click')
    await flushPromises()

    const question = wrapper.get('#delete-question')
    expect(question.find('bdi').exists()).toBe(true)
    expect(question.get('bdi').text()).toBe(RLO)

    await wrapper.get('.confirm button:last-of-type').trigger('click')
    await flushPromises()
    // The title ends the notice, so isolating the whole message is enough there.
    expect(wrapper.get('.notice bdi').text()).toContain(RLO)
  })
})

describe.each(PAYLOADS)('security: payload %s', (payload) => {
  describe('catalog page', () => {
    it('shows titles, authors and languages as text', async () => {
      stubFetch(({ url }) =>
        url.pathname === '/api/books' ? jsonResponse(200, { items: [bookWith(payload)], total: 1 }) : undefined,
      )
      const { wrapper } = await renderRoute('/books')

      expectInert(wrapper, payload)
      expect(wrapper.get('tbody a').text()).toBe(payload)
      expect(wrapper.get('tbody a').attributes('href')).toBe('/books/7')
      const cells = wrapper.findAll('tbody td').map((td) => td.text())
      expect(cells[1]).toBe(`${payload}, ${payload} 2`)
      expect(cells[3]).toBe(payload)
    })

    it('shows the author filter in the empty state as text', async () => {
      stubFetch(({ url }) =>
        url.pathname === '/api/books' ? jsonResponse(200, { items: [], total: 0 }) : undefined,
      )
      const { wrapper } = await renderRoute(`/books?author=${encodeURIComponent(payload)}`)

      expectInert(wrapper, `No books by ${payload}`)
      expect(wrapper.get<HTMLInputElement>('#author-filter').element.value).toBe(payload)
    })

    it('shows an API error message as text', async () => {
      stubFetch(() => errorResponse(500, payload))
      const { wrapper } = await renderRoute('/books')

      expectInert(wrapper, `Could not load the books: ${payload}`)
    })
  })

  describe('details page', () => {
    function stubBook(extra?: (method: string) => Response | undefined) {
      return stubFetch(({ method, url }) => {
        if (url.pathname !== '/api/books/7') return undefined
        return extra?.(method) ?? (method === 'GET' ? jsonResponse(200, bookWith(payload)) : undefined)
      })
    }

    it('shows every field as text', async () => {
      stubBook()
      const { wrapper } = await renderRoute('/books/7')

      expectInert(wrapper, payload)
      expect(wrapper.get('h1').text()).toBe(payload)
      expect(wrapper.findAll('li').map((li) => li.text())).toEqual([payload, `${payload} 2`])
    })

    it('shows the title as text in the delete confirmation', async () => {
      stubBook()
      const { wrapper } = await renderRoute('/books/7')
      await wrapper.get('.actions button').trigger('click')
      await flushPromises()

      expectInert(wrapper, `Delete "${payload}"? This cannot be undone.`)
    })

    it('shows a delete error message as text', async () => {
      stubBook((method) => (method === 'DELETE' ? errorResponse(500, payload) : undefined))
      const { wrapper } = await renderRoute('/books/7')
      await wrapper.get('.actions button').trigger('click')
      await flushPromises()
      await wrapper.get('.confirm button:last-of-type').trigger('click')
      await flushPromises()

      expectInert(wrapper, payload)
      expect(wrapper.get('.confirm [role="alert"]').text()).toBe(payload)
    })

    it('shows a load error message as text', async () => {
      stubFetch(() => errorResponse(500, payload))
      const { wrapper } = await renderRoute('/books/7')

      expectInert(wrapper, `Could not load the book: ${payload}`)
    })
  })

  describe('"Deleted <title>" notice', () => {
    it('shows the deleted title as text', async () => {
      stubFetch(({ method, url }) => {
        if (method === 'GET' && url.pathname === '/api/books/7') return jsonResponse(200, bookWith(payload))
        if (method === 'DELETE' && url.pathname === '/api/books/7') return noContent()
        if (method === 'GET' && url.pathname === '/api/books') return jsonResponse(200, { items: [], total: 0 })
        return undefined
      })
      const { wrapper } = await renderRoute('/books/7')
      await wrapper.get('.actions button').trigger('click')
      await flushPromises()
      await wrapper.get('.confirm button:last-of-type').trigger('click')
      await flushPromises()

      expect(wrapper.get('.notice').text()).toContain(`Deleted ${payload}`)
      expectInert(wrapper, `Deleted ${payload}`)
    })
  })

  describe('book form', () => {
    async function openNewForm(response: () => Response) {
      stubFetch(({ method, url }) =>
        method === 'POST' && url.pathname === '/api/books' ? response() : undefined,
      )
      const rendered = await renderRoute('/books/new')
      await rendered.wrapper.get('#book-isbn').setValue('978-0-13-235088-4')
      await rendered.wrapper.get('#book-title').setValue('Clean Code')
      await rendered.wrapper.get('#book-author-1').setValue('Robert C. Martin')
      await rendered.wrapper.get('form').trigger('submit')
      await flushPromises()
      return rendered.wrapper
    }

    it('shows a field error reason as text at the field', async () => {
      const wrapper = await openNewForm(() => errorResponse(400, `title: ${payload}`))

      expectInert(wrapper, payload)
      expect(wrapper.get('#book-title-error').text()).toBe(payload)
    })

    it('shows a top-level 400 message as text', async () => {
      const wrapper = await openNewForm(() => errorResponse(400, payload))

      expectInert(wrapper, payload)
      expect(wrapper.get('.top-error').text()).toBe(payload)
    })

    it('shows a 500 message as text', async () => {
      const wrapper = await openNewForm(() => errorResponse(500, payload))

      expectInert(wrapper, payload)
      expect(wrapper.get('.top-error').text()).toBe(payload)
    })

    it('does not show the text of a 409 at all', async () => {
      const wrapper = await openNewForm(() => errorResponse(409, payload))

      expect(findInjection(wrapper)).toEqual([])
      expect(wrapper.text()).not.toContain(payload)
      expect(wrapper.get('#book-isbn-error').text()).toBe('A book with this ISBN already exists')
    })

    it('keeps a book with the payload in the fields when revising it', async () => {
      stubFetch(({ method, url }) =>
        method === 'GET' && url.pathname === '/api/books/7' ? jsonResponse(200, bookWith(payload)) : undefined,
      )
      const { wrapper } = await renderRoute('/books/7/edit')

      expect(findInjection(wrapper)).toEqual([])
      expect(wrapper.get<HTMLInputElement>('#book-title').element.value).toBe(payload)
      expect(wrapper.get<HTMLInputElement>('#book-author-2').element.value).toBe(`${payload} 2`)
    })

    it('shows a load error of the revise form as text', async () => {
      stubFetch(() => errorResponse(500, payload))
      const { wrapper } = await renderRoute('/books/7/edit')

      expectInert(wrapper, payload)
      expect(wrapper.get('[role="alert"] p').text()).toBe(payload)
    })

    it('shows a failed "Load latest" message as text', async () => {
      let gets = 0
      stubFetch(({ method, url }) => {
        if (url.pathname !== '/api/books/7') return undefined
        if (method === 'PUT') return errorResponse(409, 'version conflict: the book was changed since it was read')
        gets += 1
        return gets === 1 ? jsonResponse(200, makeBook()) : errorResponse(500, payload)
      })
      const { wrapper } = await renderRoute('/books/7/edit')
      await wrapper.get('form').trigger('submit')
      await flushPromises()
      const loadLatest = wrapper.findAll('button').find((b) => b.text() === 'Load latest')
      await loadLatest?.trigger('click')
      await flushPromises()

      expectInert(wrapper, payload)
    })
  })
})
