import { describe, it, expect, beforeEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from './test/server'
import { makeJwt, makePlugin } from './test/factories'
import {
  api,
  ApiError,
  errMsg,
  errStatus,
  isJwtExpired,
  slugError,
  descriptionError,
  MAX_DESCRIPTION_CHARS,
} from './api'

describe('pure helpers', () => {
  describe('isJwtExpired', () => {
    it('is false for a token whose exp is in the future', () => {
      expect(isJwtExpired(makeJwt(3600))).toBe(false)
    })
    it('is true for a token whose exp is in the past', () => {
      expect(isJwtExpired(makeJwt(-3600))).toBe(true)
    })
    it('is false for a structurally malformed token', () => {
      expect(isJwtExpired('not-a-jwt')).toBe(false)
    })
    it('is false when exp is missing or non-numeric', () => {
      const noExp = `h.${btoa(JSON.stringify({ sub: 'x' }))}.s`
      const badExp = `h.${btoa(JSON.stringify({ exp: 'soon' }))}.s`
      expect(isJwtExpired(noExp)).toBe(false)
      expect(isJwtExpired(badExp)).toBe(false)
    })
  })

  describe('slugError', () => {
    it('accepts a valid slug', () => {
      expect(slugError('my-plugin')).toBe('')
    })
    it('rejects too-short / bad-shaped slugs', () => {
      expect(slugError('ab')).not.toBe('')
      expect(slugError('-leading')).not.toBe('')
      expect(slugError('UPPER')).not.toBe('')
    })
  })

  describe('descriptionError', () => {
    it('accepts a normal description', () => {
      expect(descriptionError('does a thing, use when asked')).toBe('')
    })
    it('rejects an empty or whitespace-only description', () => {
      expect(descriptionError('')).not.toBe('')
      expect(descriptionError('   \n\t ')).not.toBe('')
    })
    it('accepts a description exactly at the limit', () => {
      expect(descriptionError('a'.repeat(MAX_DESCRIPTION_CHARS))).toBe('')
    })
    it('rejects a description one character over the limit', () => {
      expect(descriptionError('a'.repeat(MAX_DESCRIPTION_CHARS + 1))).not.toBe('')
    })
    it('counts em dashes as one character, matching the SDK', () => {
      // Byte-counting would reject this: an em dash is 3 bytes in UTF-8.
      expect(descriptionError('\u2014'.repeat(MAX_DESCRIPTION_CHARS))).toBe('')
    })
  })

  describe('errMsg', () => {
    it('returns an Error message', () => {
      expect(errMsg(new Error('boom'))).toBe('boom')
    })
    it('falls back for an empty Error message', () => {
      expect(errMsg(new Error(''), 'fallback')).toBe('fallback')
    })
    it('passes through a string and falls back otherwise', () => {
      expect(errMsg('plain string')).toBe('plain string')
      expect(errMsg({ weird: true }, 'fallback')).toBe('fallback')
    })
  })

  describe('errStatus', () => {
    it('extracts the status from an ApiError', () => {
      expect(errStatus(new ApiError(404, 'nope'))).toBe(404)
    })
    it('is undefined for a non-API error', () => {
      expect(errStatus(new Error('network'))).toBeUndefined()
    })
  })
})

describe('request layer', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('parses a JSON response body', async () => {
    server.use(http.get('/api/plugins', () => HttpResponse.json([makePlugin('a')])))
    const plugins = await api.listPlugins()
    expect(plugins).toHaveLength(1)
    expect(plugins[0].name).toBe('a')
  })

  it('sets Content-Type and a Bearer token when one is stored', async () => {
    const token = makeJwt(3600)
    localStorage.setItem('token', token)
    let seen: Headers | undefined
    server.use(
      http.get('/api/me', ({ request }) => {
        seen = request.headers
        return HttpResponse.json({ id: 'u1' })
      }),
    )
    await api.me()
    expect(seen?.get('authorization')).toBe(`Bearer ${token}`)
    expect(seen?.get('content-type')).toBe('application/json')
  })

  it('omits the Authorization header when no token is stored', async () => {
    let seen: Headers | undefined
    server.use(
      http.get('/api/me', ({ request }) => {
        seen = request.headers
        return HttpResponse.json({ id: 'u1' })
      }),
    )
    await api.me()
    expect(seen?.get('authorization')).toBeNull()
  })

  it('throws an ApiError carrying status and the server "error" field', async () => {
    server.use(
      http.get('/api/plugins/missing', () =>
        HttpResponse.json({ error: 'plugin not found' }, { status: 404 }),
      ),
    )
    await expect(api.getPlugin('missing')).rejects.toMatchObject({
      name: 'ApiError',
      status: 404,
      message: 'plugin not found',
    })
  })

  it('falls back to statusText when the error body is not JSON', async () => {
    server.use(
      http.get('/api/plugins/boom', () =>
        new HttpResponse('upstream exploded', {
          status: 500,
          statusText: 'Internal Server Error',
        }),
      ),
    )
    const err = await api.getPlugin('boom').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(500)
    expect((err as ApiError).message).toBe('Internal Server Error')
  })

  it('returns undefined for a 204 No Content response', async () => {
    server.use(http.post('/api/me/sessions/revoke', () => new HttpResponse(null, { status: 204 })))
    await expect(api.revokeSessions()).resolves.toBeUndefined()
  })

  it('short-circuits an expired token without making a network call', async () => {
    localStorage.setItem('token', makeJwt(-10))
    const handler = vi.fn(() => HttpResponse.json({ id: 'u1' }))
    server.use(http.get('/api/me', handler))
    await expect(api.me()).rejects.toMatchObject({ status: 401, message: 'session expired' })
    expect(handler).not.toHaveBeenCalled()
  })

  it('forwards method and JSON body on a mutation', async () => {
    let body: unknown
    server.use(
      http.post('/api/plugins', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json(makePlugin('created'))
      }),
    )
    const created = await api.createPlugin({ name: 'created', description: 'hi' })
    expect(body).toEqual({ name: 'created', description: 'hi' })
    expect(created.name).toBe('created')
  })
})
