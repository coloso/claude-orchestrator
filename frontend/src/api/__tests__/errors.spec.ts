import { describe, expect, it } from 'vitest'
import { parseFieldError } from '@/api'
import { toApiError } from '../errors'

describe('parseFieldError', () => {
  it.each(['isbn', 'title', 'authors', 'publishedYear', 'language', 'pages'])(
    'recognizes %s',
    (field) => {
      expect(parseFieldError(`${field}: must be valid`)).toEqual({
        field,
        reason: 'must be valid',
      })
    },
  )

  it('splits a reason at the first ": " only', () => {
    expect(parseFieldError('publishedYear: must be between 1450 and 2100')).toEqual({
      field: 'publishedYear',
      reason: 'must be between 1450 and 2100',
    })
    expect(parseFieldError('title: bad: very bad')).toEqual({
      field: 'title',
      reason: 'bad: very bad',
    })
  })

  it.each([
    'author: must be valid UTF-8 without control characters',
    'limit: must be between 1 and 100',
    'offset: must be between 0 and 100000',
    'invalid request body',
    'invalid path parameter "id"',
    'unsupported content type',
    'title:no space',
    '',
  ])('gives null for %j', (message) => {
    expect(parseFieldError(message)).toBeNull()
  })
})

describe('toApiError', () => {
  it('reads the error text of an object body', () => {
    expect(toApiError(400, { error: 'isbn: invalid' })).toEqual({
      status: 400,
      kind: 'invalid',
      message: 'isbn: invalid',
    })
  })

  it.each([undefined, null, '', 'oops', 42, { error: 5 }, {}])(
    'falls back to the status for body %j',
    (body) => {
      expect(toApiError(500, body).message).toBe('Request failed with status 500')
    },
  )
})
