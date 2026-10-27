import type { components } from './schema'

export type BookField = keyof components['schemas']['BookInput']

export type ApiErrorKind =
  | 'network'
  | 'invalid'
  | 'not-found'
  | 'duplicate-isbn'
  | 'version-conflict'
  | 'other'

export interface ApiError {
  status: number
  message: string
  kind: ApiErrorKind
}

export type ApiResult<T> = { ok: true; data: T } | { ok: false; error: ApiError }

export const NETWORK_ERROR: ApiError = {
  status: 0,
  message: 'Could not reach the server.',
  kind: 'network',
}

const VERSION_CONFLICT_PREFIX = 'version conflict'

function messageOf(body: unknown, status: number): string {
  if (typeof body === 'object' && body !== null && 'error' in body) {
    const { error } = body
    if (typeof error === 'string') return error
  }
  return `Request failed with status ${status}`
}

function kindOf(status: number, message: string): ApiErrorKind {
  switch (status) {
    case 400:
      return 'invalid'
    case 404:
      return 'not-found'
    case 409:
      // The only place that tells the two 409s of an update apart.
      return message.startsWith(VERSION_CONFLICT_PREFIX) ? 'version-conflict' : 'duplicate-isbn'
    default:
      return 'other'
  }
}

/** Classifies a failed response from its status and its (already parsed, possibly empty) body. */
export function toApiError(status: number, body: unknown): ApiError {
  const message = messageOf(body, status)
  return { status, message, kind: kindOf(status, message) }
}

const BOOK_FIELDS: readonly BookField[] = [
  'isbn',
  'title',
  'authors',
  'publishedYear',
  'language',
  'pages',
]

/** Splits a 400 message `<field>: <reason>`; null when the prefix is not a form field. */
export function parseFieldError(message: string): { field: BookField; reason: string } | null {
  const at = message.indexOf(': ')
  if (at < 0) return null
  const prefix = message.slice(0, at)
  const field = BOOK_FIELDS.find((f) => f === prefix)
  if (field === undefined) return null
  return { field, reason: message.slice(at + 2) }
}
