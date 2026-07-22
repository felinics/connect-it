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
  it('2xx 返回 data', async () => {
    await expect(unwrap(result(200, { ok: true }))).resolves.toEqual({ ok: true })
  })

  it('204 返回 undefined', async () => {
    await expect(unwrap(result(204, undefined))).resolves.toBeUndefined()
  })

  it('错误响应归一为 ApiError（machine code＋message）', async () => {
    const err = await unwrap(result(409, { error: 'conflict', message: '配置已被修改' })).catch(
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).code).toBe('conflict')
    expect((err as ApiError).message).toBe('配置已被修改')
  })

  it('非 JSON 错误体回退 unknown_error', async () => {
    const err = await unwrap(result(502, undefined)).catch((e: unknown) => e)
    expect((err as ApiError).code).toBe('unknown_error')
    expect((err as ApiError).status).toBe(502)
  })

  it('401 触发全局回调；登录请求除外', async () => {
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
