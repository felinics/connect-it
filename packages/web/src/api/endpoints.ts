// 页面唯一的 API 入口：全部委托 packages/sdk 生成客户端，禁止手写 fetch。
import * as sdk from '@connect-it/sdk'

import { ApiError, unwrap } from './client'
import type { ConnectorConfig } from './types'

const adminWriteHeaders = { 'X-Connect-It-CSRF': '1' }

export const healthz = () => unwrap(sdk.healthz())

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
) => unwrap(sdk.putConfig({ path: { type }, body, headers: adminWriteHeaders }))

export const deleteConfig = (type: string) =>
  unwrap(sdk.deleteConfig({ path: { type }, headers: adminWriteHeaders }))

export const validateConfig = (
  type: string,
  body: { public: Record<string, unknown>; secrets: Record<string, string> },
) => unwrap(sdk.validateConfig({ path: { type }, body, headers: adminWriteHeaders }))

export const verifyMcp = (type: string) =>
  unwrap(sdk.verifyMcp({ path: { type }, headers: adminWriteHeaders }))

export const listConnections = () => unwrap(sdk.listConnections())

export const adminBeginOAuthConnection = (body: {
  connector_type: string
  auth_method: string
  alias?: string
}) => unwrap(sdk.adminBeginOAuthConnection({ body, headers: adminWriteHeaders }))

export const adminCreateApiKeyConnection = (body: {
  connector_type: string
  auth_method: string
  alias?: string
  fields: Record<string, string>
}) => unwrap(sdk.adminCreateApiKeyConnection({ body, headers: adminWriteHeaders }))

export const adminRecredentialConnection = (id: string, fields: Record<string, string>) =>
  unwrap(
    sdk.adminRecredentialConnection({
      path: { id },
      body: { fields },
      headers: adminWriteHeaders,
    }),
  )

export const adminDeleteConnection = (id: string) =>
  unwrap(sdk.adminDeleteConnection({ path: { id }, headers: adminWriteHeaders }))

export const adminReauthConnection = (id: string) =>
  unwrap(
    sdk.adminReauthConnection({
      path: { id },
      headers: adminWriteHeaders,
    }),
  )

export const listApiTokens = () => unwrap(sdk.listApiTokens())

export const createApiToken = (name: string) =>
  unwrap(sdk.createApiToken({ body: { name }, headers: adminWriteHeaders }))

export const deleteApiToken = (id: string) =>
  unwrap(sdk.deleteApiToken({ path: { id }, headers: adminWriteHeaders }))

export const changePassword = (password: string) =>
  unwrap(sdk.changePassword({ body: { password }, headers: adminWriteHeaders }))
