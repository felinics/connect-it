import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

import { listConnectors, updateConnectorEnabled } from '../api/endpoints'
import en from '../i18n/en'
import ConnectorsPage from './connectors.vue'

const { toastSuccess } = vi.hoisted(() => ({ toastSuccess: vi.fn() }))

vi.mock('@felinic/ui', () => ({
  ActionCard: {
    props: ['title', 'description'],
    template: '<article><slot name="icon" /><span>{{ title }}</span><slot name="trailing" /></article>',
  },
  Input: { template: '<input />' },
  SegmentedControl: { template: '<div />' },
  Switch: {
    props: ['modelValue', 'disabled'],
    emits: ['update:modelValue'],
    template: '<button data-switch :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)" />',
  },
  toast: { error: vi.fn(), success: toastSuccess },
}))

vi.mock('../api/endpoints', () => ({
  listConnectors: vi.fn(),
  updateConnectorEnabled: vi.fn(),
}))

const listConnectorsMock = vi.mocked(listConnectors)
const updateConnectorEnabledMock = vi.mocked(updateConnectorEnabled)
const i18n = createI18n({
  legacy: false,
  locale: 'en',
  messages: { en },
})

describe('connectors enabled switch', () => {
  beforeEach(() => {
    listConnectorsMock.mockReset()
    updateConnectorEnabledMock.mockReset()
    toastSuccess.mockReset()
  })

  it('persists the new state and refreshes the catalog item', async () => {
    listConnectorsMock.mockResolvedValue([
      { type: 'github', name: 'GitHub', description: 'GitHub connector', enabled: true, status: 'ready' },
    ])
    updateConnectorEnabledMock.mockResolvedValue({
      type: 'github', name: 'GitHub', description: 'GitHub connector', enabled: false, status: 'disabled',
    })
    const wrapper = mount(ConnectorsPage, {
      global: {
        plugins: [i18n],
        stubs: {
          PageShell: { template: '<main><slot /></main>' },
          ProviderLogo: true,
          StatusBadge: { props: ['status'], template: '<span>{{ status }}</span>' },
          ChevronRightIcon: true,
        },
      },
    })

    await flushPromises()
    await wrapper.get('[data-switch]').trigger('click')
    await flushPromises()

    expect(updateConnectorEnabledMock).toHaveBeenCalledWith('github', false)
    expect(wrapper.text()).toContain('disabled')
    expect(toastSuccess).toHaveBeenCalledWith('GitHub disabled')
  })
})
