import { client } from '@connect-it/sdk'

// Uniform errors: the backend returns {"error":"machine_code","message":"human readable"}.
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

// unwrap normalises the {data,error,response} triple from the generated SDK:
// 2xx yields data, anything else throws ApiError, and a 401 (except on the
// login request) triggers the global redirect to login.
export async function unwrap<T>(
  promise: Promise<SdkResult<T>>,
  opts: { allowUnauthorized?: boolean } = {},
): Promise<T> {
  const { data, error, response } = await promise
  if (response.ok) {
    return data as T
  }
  let code = 'unknown_error'
  let message = `Request failed (HTTP ${response.status})`
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
