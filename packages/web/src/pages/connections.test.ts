import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import en from '../i18n/en'
import ConnectionsPage from './connections.vue'

const api = vi.hoisted(() => ({
  adminBeginOAuthConnection: vi.fn(),
  adminCreateApiKeyConnection: vi.fn(),
  adminDeleteConnection: vi.fn(),
  adminReauthConnection: vi.fn(),
  adminRecredentialConnection: vi.fn(),
  listAuthMethods: vi.fn(),
  listConnections: vi.fn(),
  listConnectors: vi.fn(),
}))

const toast = vi.hoisted(() => ({
  error: vi.fn(),
  success: vi.fn(),
}))

vi.mock('../api/endpoints', () => api)
vi.mock('@felinic/ui', async () => {
  const actual = await vi.importActual<typeof import('@felinic/ui')>('@felinic/ui')
  return { ...actual, toast }
})

const datadogMethods = [
  {
    key: 'api_keys',
    label: 'API and application keys',
    type: 'custom_credential',
    credential_fields: [
      {
        key: 'api_key',
        label: 'API key',
        input_type: 'text',
        required: true,
        secret: true,
        options: [],
      },
      {
        key: 'application_key',
        label: 'Application key',
        input_type: 'text',
        required: true,
        secret: true,
        options: [],
      },
      {
        key: 'site',
        label: 'Site',
        input_type: 'select',
        required: true,
        secret: false,
        default_value: 'us1',
        options: ['us1', 'eu'],
      },
    ],
  },
]

const githubMethods = [
  {
    key: 'oauth',
    label: 'OAuth',
    type: 'oauth2',
    credential_fields: [],
  },
]

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/connections', component: ConnectionsPage }],
  })
  await router.push('/connections')
  await router.isReady()

  const i18n = createI18n({
    legacy: false,
    locale: 'en',
    messages: { en },
  })
  const wrapper = mount(ConnectionsPage, {
    global: { plugins: [router, i18n] },
  })
  await flushPromises()
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  api.adminBeginOAuthConnection.mockResolvedValue({
    authorization_url: 'https://github.com/login/oauth/authorize?state=state-value',
    connection_id: 'new-oauth',
  })
  api.adminCreateApiKeyConnection.mockResolvedValue({ connection_id: 'new-credential' })
  api.adminDeleteConnection.mockResolvedValue(undefined)
  api.adminReauthConnection.mockResolvedValue({
    authorization_url: 'https://github.com/login/oauth/authorize?state=reauth-state',
    connection_id: 'oauth-1',
  })
  api.adminRecredentialConnection.mockResolvedValue({})
  api.listConnections.mockResolvedValue([])
  api.listConnectors.mockResolvedValue([])
  api.listAuthMethods.mockImplementation((type: string) =>
    Promise.resolve(
      type === 'github'
        ? githubMethods
        : datadogMethods,
    ),
  )
})

describe('connections management page', () => {
  it('uses auth metadata to show OAuth reauth and credential replacement exclusively', async () => {
    api.listConnectors.mockResolvedValue([
      { type: 'github', name: 'GitHub' },
      { type: 'datadog', name: 'Datadog' },
    ])
    api.listConnections.mockResolvedValue([
      {
        id: 'oauth-1',
        alias: 'GitHub production',
        connector_type: 'github',
        auth_method: 'oauth',
        status: 'active',
      },
      {
        id: 'credential-1',
        alias: 'Datadog production',
        connector_type: 'datadog',
        auth_method: 'api_keys',
        status: 'active',
      },
    ])

    const wrapper = await mountPage()

    expect(wrapper.find('[data-testid="reauth-oauth-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="replace-oauth-1"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="replace-credential-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="reauth-credential-1"]').exists()).toBe(false)

    await wrapper.get('[data-testid="replace-credential-1"]').trigger('click')
    await wrapper.get('#replace-credential-api_key').setValue('replacement-api')
    await wrapper.get('#replace-credential-application_key').setValue('replacement-app')
    await wrapper.get('[data-testid="replace-credentials-section"] form').trigger('submit')
    await flushPromises()

    expect(api.adminRecredentialConnection).toHaveBeenCalledWith('credential-1', {
      api_key: 'replacement-api',
      application_key: 'replacement-app',
      site: 'us1',
    })
    expect(wrapper.find('[data-testid="replace-credentials-section"]').exists()).toBe(false)
  })

  it('creates a custom-credential connection from the full Definition-driven field group', async () => {
    api.listConnectors.mockResolvedValue([{ type: 'datadog', name: 'Datadog' }])

    const wrapper = await mountPage()
    expect(wrapper.find('[data-testid="create-credential-form"]').exists()).toBe(true)

    await wrapper.get('#create-alias').setValue('  production-metrics  ')
    await wrapper.get('#create-credential-api_key').setValue('api-secret')
    await wrapper.get('#create-credential-application_key').setValue('application-secret')
    await wrapper.get('[data-testid="create-credential-form"]').trigger('submit')
    await flushPromises()

    expect(api.adminCreateApiKeyConnection).toHaveBeenCalledWith({
      connector_type: 'datadog',
      auth_method: 'api_keys',
      alias: 'production-metrics',
      fields: {
        api_key: 'api-secret',
        application_key: 'application-secret',
        site: 'us1',
      },
    })
    expect(wrapper.get<HTMLInputElement>('#create-credential-api_key').element.value).toBe('')
    expect(wrapper.text()).not.toContain('api-secret')
    expect(wrapper.text()).not.toContain('application-secret')
  })

  it('validates the optional alias before sending credentials', async () => {
    api.listConnectors.mockResolvedValue([{ type: 'datadog', name: 'Datadog' }])

    const wrapper = await mountPage()
    await wrapper.get('#create-alias').setValue('Invalid Alias')
    await wrapper.get('#create-credential-api_key').setValue('api-secret')
    await wrapper.get('#create-credential-application_key').setValue('application-secret')
    await wrapper.get('[data-testid="create-credential-form"]').trigger('submit')
    await flushPromises()

    expect(api.adminCreateApiKeyConnection).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain(
      'Use no more than 32 lowercase letters, digits, and hyphens.',
    )
  })

  it('keeps the OAuth URL out of rendered text and opens or copies it explicitly', async () => {
    api.listConnectors.mockResolvedValue([{ type: 'github', name: 'GitHub' }])
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })
    const open = vi.spyOn(window, 'open').mockReturnValue(null)

    const wrapper = await mountPage()
    await wrapper.get('#create-alias').setValue('primary-source-control')
    await wrapper.get('[data-testid="start-oauth"]').trigger('submit')
    await flushPromises()

    expect(api.adminBeginOAuthConnection).toHaveBeenCalledWith({
      connector_type: 'github',
      auth_method: 'oauth',
      alias: 'primary-source-control',
    })
    expect(wrapper.text()).toContain('github.com')
    expect(wrapper.text()).not.toContain('state-value')

    const linkSection = wrapper.get('[data-testid="oauth-link-section"]')
    const buttons = linkSection.findAll('button')
    await buttons.find((button) => button.text() === 'Copy authorization link')!.trigger('click')
    buttons.find((button) => button.text() === 'Open authorization page')!.trigger('click')

    expect(writeText).toHaveBeenCalledWith(
      'https://github.com/login/oauth/authorize?state=state-value',
    )
    expect(open).toHaveBeenCalledWith(
      'https://github.com/login/oauth/authorize?state=state-value',
      '_blank',
      'noopener,noreferrer',
    )
    open.mockRestore()
  })

  it('reauthorizes an OAuth connection without a request body', async () => {
    api.listConnectors.mockResolvedValue([{ type: 'github', name: 'GitHub' }])
    api.listConnections.mockResolvedValue([
      {
        id: 'github-1',
        alias: 'Source',
        connector_type: 'github',
        auth_method: 'oauth',
        status: 'active',
      },
    ])

    const wrapper = await mountPage()
    await wrapper.get('[data-testid="reauth-github-1"]').trigger('click')
    await flushPromises()
    expect(api.adminReauthConnection).toHaveBeenCalledWith('github-1')
  })

  it.each([
    ['non-HTTPS', 'http://attacker.example/authorize?state=value'],
    ['userinfo-bearing', 'https://user:password@attacker.example/authorize?state=value'],
  ])('rejects a %s OAuth URL returned by the server', async (_case, authorizationURL) => {
    api.listConnectors.mockResolvedValue([{ type: 'github', name: 'GitHub' }])
    api.adminBeginOAuthConnection.mockResolvedValue({
      authorization_url: authorizationURL,
      connection_id: 'unsafe',
    })

    const wrapper = await mountPage()
    await wrapper.get('[data-testid="start-oauth"]').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).not.toContain('attacker.example')
    expect(toast.error).toHaveBeenCalledWith(
      'The server returned an unsafe or invalid authorization link',
    )
  })
})
