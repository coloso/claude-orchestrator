import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, type VueWrapper } from '@vue/test-utils'

import type { BookField } from '@/api'
import { errorResponse, jsonResponse, makeBook, renderRoute, stubFetch } from '@/__tests__/helpers'

afterEach(() => {
  vi.unstubAllGlobals()
})

const FULL_BOOK = makeBook({
  id: 7,
  authors: ['Andrew Hunt', 'David Thomas', 'Ward Cunningham'],
  publishedYear: 1999,
  language: 'en',
  pages: 352,
})

/** Serves the list and book 7; `save` answers the POST and the PUT. */
function stubCatalog(save: () => Response = () => errorResponse(501, 'not expected')) {
  return stubFetch(({ method, url }) => {
    if (method === 'GET' && url.pathname === '/api/books') {
      return jsonResponse(200, { items: [FULL_BOOK, makeBook({ id: 8, title: 'Other' })], total: 2 })
    }
    if (method === 'GET' && url.pathname === '/api/books/7') return jsonResponse(200, FULL_BOOK)
    if (method === 'POST' || method === 'PUT') return save()
    return undefined
  })
}

/** Text of an element without what is hidden from assistive technology. */
function visibleText(element: Element): string {
  const copy = element.cloneNode(true) as Element
  copy.querySelectorAll('[aria-hidden="true"]').forEach((hidden) => hidden.remove())
  return (copy.textContent ?? '').trim()
}

function labelTexts(root: ParentNode, control: Element): string[] {
  const texts: string[] = []
  if (control.id !== '') {
    root.querySelectorAll('label').forEach((label) => {
      if (label.htmlFor === control.id) texts.push(visibleText(label))
    })
  }
  const wrapping = control.closest('label')
  if (wrapping !== null) texts.push(visibleText(wrapping))
  return texts
}

function assertAccessiblePage(wrapper: VueWrapper) {
  const root = wrapper.element

  const controls = [...root.querySelectorAll('input, select, textarea')]
  expect(controls.length).toBeGreaterThan(0)
  for (const control of controls) {
    const label = labelTexts(root, control).find((text) => text !== '')
    expect(label, `no label for <${control.tagName.toLowerCase()} id="${control.id}">`).toBeDefined()
  }

  const ids = [...root.querySelectorAll('[id]')].map((element) => element.id)
  expect(ids.filter((id, index) => ids.indexOf(id) !== index)).toEqual([])

  const buttons = [...root.querySelectorAll('button')]
  expect(buttons.length).toBeGreaterThan(0)
  for (const control of buttons) {
    const name = (control.getAttribute('aria-label') ?? '').trim() || visibleText(control)
    expect(name, `button without a name: ${control.outerHTML}`).not.toBe('')
  }
}

describe('labels, ids and button names', () => {
  it.each(['/books', '/books/new', '/books/7/edit'])('%s is accessible', async (path) => {
    stubCatalog()
    const { wrapper } = await renderRoute(path)
    await vi.waitFor(() => expect(wrapper.find('input').exists()).toBe(true))
    await vi.waitFor(() => expect(wrapper.text()).not.toContain('Loading'))

    assertAccessiblePage(wrapper)
  })

  it('/books/7/edit has an input for each of the three authors', async () => {
    stubCatalog()
    const { wrapper } = await renderRoute('/books/7/edit')
    await vi.waitFor(() => expect(wrapper.find('#book-author-3').exists()).toBe(true))

    expect(wrapper.findAll('fieldset input')).toHaveLength(3)
    assertAccessiblePage(wrapper)
  })
})

const FIELDS: readonly BookField[] = ['isbn', 'title', 'authors', 'publishedYear', 'language', 'pages']

/** The input or inputs that belong to a field of the book form. */
function inputsOf(wrapper: VueWrapper, field: BookField): Element[] {
  const selector = field === 'authors' ? 'fieldset input' : `#book-${field}`
  return [...wrapper.element.querySelectorAll(selector)]
}

function assertFieldError(wrapper: VueWrapper, field: BookField, reason: string) {
  const inputs = inputsOf(wrapper, field)
  expect(inputs.length).toBeGreaterThan(0)
  for (const input of inputs) {
    expect(input.getAttribute('aria-invalid')).toBe('true')
    const describedBy = (input.getAttribute('aria-describedby') ?? '').split(/\s+/).filter((id) => id !== '')
    expect(describedBy.length).toBeGreaterThan(0)
    const text = describedBy
      .map((id) => {
        const target = wrapper.element.querySelector(`[id="${id}"]`)
        expect(target, `aria-describedby points to missing id "${id}"`).not.toBeNull()
        return target?.textContent ?? ''
      })
      .join(' ')
    expect(text).toContain(reason)
  }
  for (const other of FIELDS.filter((f) => f !== field)) {
    for (const input of inputsOf(wrapper, other)) {
      expect(input.getAttribute('aria-invalid')).toBeNull()
    }
  }
}

const MODES = [
  { mode: 'add', path: '/books/new' },
  { mode: 'edit', path: '/books/7/edit' },
] as const

async function openForm(path: string, save: () => Response) {
  stubCatalog(save)
  const { wrapper } = await renderRoute(path)
  await vi.waitFor(() => expect(wrapper.find('#book-isbn').exists()).toBe(true))
  return wrapper
}

async function submit(wrapper: VueWrapper) {
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

describe.each(MODES)('field errors in $mode mode', ({ path }) => {
  it.each(FIELDS)('links the 400 error on %s to its input', async (field) => {
    const wrapper = await openForm(path, () => errorResponse(400, `${field}: some reason`))
    await submit(wrapper)

    assertFieldError(wrapper, field, 'some reason')
  })

  it('links the duplicate ISBN error to the ISBN input', async () => {
    const wrapper = await openForm(path, () => errorResponse(409, 'a book with this isbn already exists'))
    await submit(wrapper)

    assertFieldError(wrapper, 'isbn', 'already exists')
  })

  it('announces a 400 that names no field in an alert', async () => {
    const wrapper = await openForm(path, () => errorResponse(400, 'the request could not be processed'))
    await submit(wrapper)

    const alerts = wrapper.findAll('[role="alert"]')
    expect(alerts.some((alert) => alert.text().includes('the request could not be processed'))).toBe(true)
    expect(wrapper.element.querySelector('[aria-invalid="true"]')).toBeNull()
  })
})
