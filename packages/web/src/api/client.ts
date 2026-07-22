import { client } from '@connect-it/sdk'

// 统一错误：后端约定 {"error":"machine_code","message":"人类可读"}。
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

let unauthorizedHandler: (() => void) | null = null

export function setUnauthorizedHandler(fn: (() => void) | null) {
  unauthorizedHandler = fn
}

client.setConfig({ baseUrl: '', credentials: 'include' })

interface SdkResult<T> {
  data?: T
  error?: unknown
  response: Response
}

// unwrap 把生成 SDK 的 {data,error,response} 归一：2xx 返回 data，
// 非 2xx 抛 ApiError；401（登录请求除外）触发全局跳登录。
export async function unwrap<T>(
  promise: Promise<SdkResult<T>>,
  opts: { allowUnauthorized?: boolean } = {},
): Promise<T> {
  const { data, error, response } = await promise
  if (response.ok) {
    return data as T
  }
  let code = 'unknown_error'
  let message = `请求失败（HTTP ${response.status}）`
  if (error && typeof error === 'object') {
    const e = error as { error?: string; message?: string }
    if (typeof e.error === 'string' && e.error !== '') code = e.error
    if (typeof e.message === 'string' && e.message !== '') message = e.message
  }
  if (response.status === 401 && !opts.allowUnauthorized) {
    unauthorizedHandler?.()
  }
  throw new ApiError(response.status, code, message)
}
