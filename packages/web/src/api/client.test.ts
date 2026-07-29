import { describe, expect, it } from 'vitest'

import { ApiError, setUnauthorizedHandler, unwrap } from './client'

function result(status: number, body: unknown) {
  return Promise.resolve({
    data: status < 300 ? body : undefined,
    error: status >= 300 ? body : undefined,
    response: new Response(null, { status: status === 204 ? 204 : status }),
  })
}

describe('unwrap', () => {
  it('returns data on 2xx', async () => {
    await expect(unwrap(result(200, { ok: true }))).resolves.toEqual({ ok: true })
  })

  it('returns undefined on 204', async () => {
    await expect(unwrap(result(204, undefined))).resolves.toBeUndefined()
  })

  it('normalises an error response into ApiError with machine code and message', async () => {
    const err = await unwrap(result(409, { error: 'conflict', message: 'the config was modified' })).catch(
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).code).toBe('conflict')
    expect((err as ApiError).message).toBe('the config was modified')
  })

  it('falls back to unknown_error on a non-JSON error body', async () => {
    const err = await unwrap(result(502, undefined)).catch((e: unknown) => e)
    expect((err as ApiError).code).toBe('unknown_error')
    expect((err as ApiError).status).toBe(502)
  })

  it('fires the global callback on 401, except for the login request', async () => {
    let called = 0
    setUnauthorizedHandler(() => {
      called += 1
    })
    await unwrap(result(401, { error: 'unauthorized', message: 'x' })).catch(() => undefined)
    expect(called).toBe(1)
    await unwrap(result(401, { error: 'invalid_credentials', message: 'x' }), {
      allowUnauthorized: true,
    }).catch(() => undefined)
    expect(called).toBe(1)
    setUnauthorizedHandler(null)
  })
})
