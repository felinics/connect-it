import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

import { getVersion } from '../api/endpoints'
import en from '../i18n/en'
import SettingsPage from './settings.vue'

vi.mock('@felinic/ui', () => ({
  Button: { template: '<button><slot /></button>' },
  Input: { template: '<input />' },
  Label: { template: '<label><slot /></label>' },
  toast: { success: vi.fn() },
}))

vi.mock('../api/endpoints', () => ({
  changePassword: vi.fn(),
  getVersion: vi.fn(),
}))

const getVersionMock = vi.mocked(getVersion)
const i18n = createI18n({
  legacy: false,
  locale: 'en',
  messages: { en },
})

const stubs = {
  PageShell: {
    props: ['title'],
    template: '<main><h1>{{ title }}</h1><slot /></main>',
  },
  SettingsSection: {
    props: ['title'],
    template: '<section><h2>{{ title }}</h2><slot /></section>',
  },
  SettingsRow: {
    props: ['label', 'description'],
    template: '<div><span>{{ label }}</span><span>{{ description }}</span><slot /></div>',
  },
  Button: true,
  Input: true,
  Label: true,
}

describe('settings version', () => {
  beforeEach(() => {
    getVersionMock.mockReset()
  })

  it('shows the version returned by the service', async () => {
    getVersionMock.mockResolvedValue({ version: '1.2.3' })
    const wrapper = shallowMount(SettingsPage, {
      global: { plugins: [i18n], stubs },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('About')
    expect(wrapper.text()).toContain('Version')
    expect(wrapper.text()).toContain('1.2.3')
  })

  it('shows an unavailable state when the service cannot report a version', async () => {
    getVersionMock.mockRejectedValue(new Error('offline'))
    const wrapper = shallowMount(SettingsPage, {
      global: { plugins: [i18n], stubs },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('Unavailable')
  })
})
