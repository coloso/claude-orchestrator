import { describe, expect, it } from 'vitest'
import { flushPromises } from '@vue/test-utils'

import { renderRoute, stubFetch, jsonResponse } from '@/__tests__/helpers'
import { useNoticeStore } from '@/stores/notice'
import BookDetailView from '@/views/BookDetailView.vue'
import BookFormView from '@/views/BookFormView.vue'

describe('routing', () => {
  it('redirects / to /books and links to it from the header', async () => {
    const { wrapper, router } = await renderRoute('/')
    expect(router.currentRoute.value.fullPath).toBe('/books')
    expect(wrapper.find('header a').attributes('href')).toBe('/books')
    expect(wrapper.find('h1').text()).toBe('Catalog')
  })

  it.each(['/nope', '/books/abc', '/books/0'])('shows "Page not found" for %s', async (path) => {
    const { wrapper } = await renderRoute(path)
    expect(wrapper.find('h1').text()).toBe('Page not found')
    expect(wrapper.find('main a').attributes('href')).toBe('/books')
  })

  it('renders the detail view with a numeric id', async () => {
    const { wrapper } = await renderRoute('/books/7')
    expect(wrapper.findComponent(BookDetailView).props('id')).toBe(7)
  })

  it('renders the form in edit mode with the id', async () => {
    const { wrapper } = await renderRoute('/books/7/edit')
    expect(wrapper.findComponent(BookFormView).props('id')).toBe(7)
  })

  it('renders the form in add mode without an id', async () => {
    const { wrapper } = await renderRoute('/books/new')
    expect(wrapper.findComponent(BookFormView).props('id')).toBeUndefined()
  })

  it('remounts a view when the path changes but not when only the query changes', async () => {
    const { wrapper, router } = await renderRoute('/books/7')
    const first = wrapper.findComponent(BookDetailView).vm.$.uid
    await router.push('/books/8')
    await flushPromises()
    const second = wrapper.findComponent(BookDetailView).vm.$.uid
    expect(second).not.toBe(first)
    expect(wrapper.findComponent(BookDetailView).props('id')).toBe(8)

    await router.push('/books/8?x=1')
    await flushPromises()
    expect(wrapper.findComponent(BookDetailView).vm.$.uid).toBe(second)
  })
})

describe('notice', () => {
  it('shows the message in a status region and dismisses it', async () => {
    const { wrapper } = await renderRoute('/books')
    expect(wrapper.find('[role="status"]').exists()).toBe(false)

    useNoticeStore().show('Deleted Clean Code')
    await flushPromises()
    expect(wrapper.find('[role="status"]').text()).toContain('Deleted Clean Code')

    await wrapper.find('[role="status"] button').trigger('click')
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    expect(useNoticeStore().message).toBeNull()
  })

  it('is cleared by the next navigation', async () => {
    const { wrapper, router } = await renderRoute('/books')
    useNoticeStore().show('Deleted Clean Code')
    await router.push('/books/new')
    await flushPromises()
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
  })

  it('stays visible when set after the push has resolved', async () => {
    const { wrapper, router } = await renderRoute('/books/7')
    await router.push('/books')
    useNoticeStore().show('Deleted Clean Code')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/books')
    expect(wrapper.find('[role="status"]').text()).toContain('Deleted Clean Code')
  })
})

describe('stubFetch', () => {
  it('records method, URL and JSON body', async () => {
    const requests = stubFetch(() => jsonResponse(200, { ok: true }))
    const response = await fetch('/api/books?limit=5', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title: 'Clean Code' }),
    })
    await fetch(new Request('http://localhost/api/books/7'))

    expect(response.status).toBe(200)
    expect(requests).toHaveLength(2)
    expect(requests[0]?.method).toBe('POST')
    expect(requests[0]?.url.pathname).toBe('/api/books')
    expect(requests[0]?.url.searchParams.get('limit')).toBe('5')
    expect(requests[0]?.body).toEqual({ title: 'Clean Code' })
    expect(requests[1]?.method).toBe('GET')
    expect(requests[1]?.body).toBeUndefined()
  })

  it('answers 501 when the handler returns undefined', async () => {
    stubFetch(() => undefined)
    const response = await fetch('/api/books?limit=5')
    expect(response.status).toBe(501)
    expect(await response.json()).toEqual({
      error: 'unexpected request in test: GET /api/books?limit=5',
    })
  })

  it('rejects when the handler throws', async () => {
    stubFetch(() => {
      throw new TypeError('Failed to fetch')
    })
    await expect(fetch('/api/books')).rejects.toThrow('Failed to fetch')
  })

  it('blocks network access unless fetch is stubbed', async () => {
    await expect(fetch('/api/books')).rejects.toThrow('unexpected network access in test')
  })
})
