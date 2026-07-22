// 页面唯一的 API 入口：全部委托 packages/sdk 生成客户端，禁止手写 fetch。
import * as sdk from '@connect-it/sdk'

import { ApiError, unwrap } from './client'
import type { ConnectorConfig } from './types'

export const login = (username: string, password: string) =>
  unwrap(sdk.login({ body: { username, password } }), { allowUnauthorized: true })

export const listConnectors = () => unwrap(sdk.adminListConnectors())

export const getConfigSchema = (type: string) =>
  unwrap(sdk.getConfigSchema({ path: { type } }))

export const listAuthMethods = (type: string) =>
  unwrap(sdk.listAuthMethods({ path: { type } }))

// 未配置返回 null（404 不是错误态）。
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

export const validateConfig = (
  type: string,
  body: { public: Record<string, unknown>; secrets: Record<string, string> },
) => unwrap(sdk.validateConfig({ path: { type }, body }))

export const verifyMcp = (type: string) => unwrap(sdk.verifyMcp({ path: { type } }))

export const listConnections = () => unwrap(sdk.listConnections())

export const startOAuth = (body: { connector_type: string; auth_method: string; alias: string }) =>
  unwrap(sdk.startOAuth({ body }))

export const createApiKeyConnection = (body: {
  connector_type: string
  auth_method: string
  alias: string
  fields: Record<string, string>
}) => unwrap(sdk.createApiKeyConnection({ body }))

export const deleteConnection = (id: string) => unwrap(sdk.deleteConnection({ path: { id } }))

export const reauthConnection = (id: string) => unwrap(sdk.reauthConnection({ path: { id } }))

export const listApiTokens = () => unwrap(sdk.listApiTokens())

export const createApiToken = (name: string) => unwrap(sdk.createApiToken({ body: { name } }))

export const deleteApiToken = (id: string) => unwrap(sdk.deleteApiToken({ path: { id } }))

export const changePassword = (password: string) =>
  unwrap(sdk.changePassword({ body: { password } }))
