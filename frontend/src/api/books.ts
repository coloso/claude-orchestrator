import { client } from './client'
import { NETWORK_ERROR, toApiError, type ApiResult } from './errors'
import type { components, paths } from './schema'

export type Book = components['schemas']['Book']
export type BookInput = components['schemas']['BookInput']
export type BookUpdate = components['schemas']['BookUpdate']
export type BookList = components['schemas']['BookList']
export type ListBooksQuery = NonNullable<paths['/api/books']['get']['parameters']['query']>

export const PAGE_SIZE = 20

interface Reply {
  data?: unknown
  error?: unknown
  response: Response
}

// Decides on response.status, not on which of data/error openapi-fetch filled in: empty
// bodies (a 204, an error with Content-Length 0) give neither. A rejected fetch never escapes.
async function call<T>(send: () => Promise<Reply>, empty?: T): Promise<ApiResult<T>> {
  try {
    const { data, error, response } = await send()
    if (!response.ok) return { ok: false, error: toApiError(response.status, error) }
    if (data !== undefined) return { ok: true, data: data as T }
    if (empty !== undefined) return { ok: true, data: empty }
    return { ok: false, error: toApiError(response.status, undefined) }
  } catch {
    return { ok: false, error: NETWORK_ERROR }
  }
}

export function listBooks(query: ListBooksQuery): Promise<ApiResult<BookList>> {
  const { author, limit, offset } = query
  const params: ListBooksQuery = {}
  if (author !== undefined && author !== '') params.author = author
  if (limit !== undefined) params.limit = limit
  if (offset !== undefined) params.offset = offset
  return call(() => client.GET('/api/books', { params: { query: params } }))
}

export function getBook(id: number): Promise<ApiResult<Book>> {
  return call(() => client.GET('/api/books/{id}', { params: { path: { id } } }))
}

export function createBook(input: BookInput): Promise<ApiResult<Book>> {
  return call(() => client.POST('/api/books', { body: input }))
}

export function updateBook(id: number, input: BookUpdate): Promise<ApiResult<Book>> {
  return call(() => client.PUT('/api/books/{id}', { params: { path: { id } }, body: input }))
}

export function deleteBook(id: number): Promise<ApiResult<null>> {
  return call(() => client.DELETE('/api/books/{id}', { params: { path: { id } } }), null)
}
