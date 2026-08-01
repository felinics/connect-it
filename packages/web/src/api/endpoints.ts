// The only API entry point for pages: everything goes through the generated
// client in packages/sdk. Hand-written fetch calls are not allowed.
import * as sdk from '@connect-it/sdk'

import { ApiError, unwrap } from './client'
import type { ConnectorConfig } from './types'

export const healthz = () => unwrap(sdk.healthz())

export const getVersion = () => unwrap(sdk.getVersion())

export const login = (username: string, password: string) =>
  unwrap(sdk.login({ body: { username, password } }), { allowUnauthorized: true })

export const listConnectors = () => unwrap(sdk.adminListConnectors())

export const getConfigSchema = (type: string) =>
  unwrap(sdk.getConfigSchema({ path: { type } }))

export const listAuthMethods = (type: string) =>
  unwrap(sdk.listAuthMethods({ path: { type } }))

// Returns null when unconfigured; a 404 is not an error state here.
export async function getConfig(type: string): Promise<ConnectorConfig | null> {
  try {
    return await unwrap(sdk.getConfig({ path: { type } }))
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null
    throw e
  }
}

export const putConfig = (
  type: string,
  body: { public: Record<string, unknown>; secrets: Record<string, string>; if_match?: string },
) => unwrap(sdk.putConfig({ path: { type }, body }))

export const deleteConfig = (type: string) => unwrap(sdk.deleteConfig({ path: { type } }))

export const listConnections = () => unwrap(sdk.listConnections())

export const adminDeleteConnection = (id: string) =>
  unwrap(sdk.adminDeleteConnection({ path: { id } }))

export const adminReauthConnection = (id: string) =>
  unwrap(sdk.adminReauthConnection({ path: { id } }))

export const listApiTokens = () => unwrap(sdk.listApiTokens())

export const createApiToken = (name: string) => unwrap(sdk.createApiToken({ body: { name } }))

export const deleteApiToken = (id: string) => unwrap(sdk.deleteApiToken({ path: { id } }))

export const changePassword = (password: string) =>
  unwrap(sdk.changePassword({ body: { password } }))
