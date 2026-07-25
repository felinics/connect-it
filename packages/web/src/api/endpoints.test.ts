import { beforeEach, describe, expect, it, vi } from 'vitest'

const sdk = vi.hoisted(() => ({
  adminBeginOAuthConnection: vi.fn(),
  adminReauthConnection: vi.fn(),
  client: { setConfig: vi.fn() },
}))

vi.mock('@connect-it/sdk', () => sdk)

import {
  adminBeginOAuthConnection,
  adminReauthConnection,
} from './endpoints'

function ok<T>(data: T) {
  return Promise.resolve({
    data,
    response: new Response(null, { status: 200 }),
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  sdk.adminBeginOAuthConnection.mockReturnValue(ok({ connection_id: 'oauth' }))
  sdk.adminReauthConnection.mockReturnValue(ok({ connection_id: 'reauth' }))
})

describe('authorization endpoints', () => {
  it('passes standard OAuth fields to the generated SDK method', async () => {
    await adminBeginOAuthConnection({
      connector_type: 'github',
      auth_method: 'oauth',
    })
    expect(sdk.adminBeginOAuthConnection).toHaveBeenCalledWith({
      body: {
        connector_type: 'github',
        auth_method: 'oauth',
      },
      headers: { 'X-Connect-It-CSRF': '1' },
    })
  })

  it('reauthorizes without a request body', async () => {
    await adminReauthConnection('connection-1')
    expect(sdk.adminReauthConnection).toHaveBeenCalledWith({
      path: { id: 'connection-1' },
      headers: { 'X-Connect-It-CSRF': '1' },
    })
  })
})
