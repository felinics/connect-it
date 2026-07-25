import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'

import type { ConfigField } from '../api/types'
import en from '../i18n/en'
import CredentialFieldsForm from './CredentialFieldsForm.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  messages: { en },
})

function mountForm(fields: ConfigField[]) {
  return mount(CredentialFieldsForm, {
    props: {
      fields,
      idPrefix: 'credential',
      submitLabel: 'Submit credentials',
    },
    global: { plugins: [i18n] },
  })
}

describe('CredentialFieldsForm', () => {
  it('renders secret/select metadata, validates, and submits the complete exact field group', async () => {
    const wrapper = mountForm([
      {
        key: 'api_key',
        label: 'API key',
        input_type: 'text',
        required: true,
        secret: true,
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
      {
        key: 'account',
        label: 'Account',
        input_type: 'text',
        required: true,
        secret: false,
        pattern: '^[a-z]+$',
      },
    ])

    const secret = wrapper.get<HTMLInputElement>('#credential-api_key')
    expect(secret.attributes('type')).toBe('password')
    expect(secret.attributes('autocomplete')).toBe('new-password')
    expect(secret.element.value).toBe('')
    expect(wrapper.get<HTMLSelectElement>('#credential-site').element.value).toBe('us1')

    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('submit')).toBeUndefined()
    expect(wrapper.text()).toContain('Required')

    await secret.setValue('  exact-secret  ')
    await wrapper.get('#credential-account').setValue('NOT-VALID')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('submit')).toBeUndefined()
    expect(wrapper.text()).toContain('Must match ^[a-z]+$')

    await wrapper.get('#credential-account').setValue('primary')
    await wrapper.get('form').trigger('submit')

    expect(wrapper.emitted('submit')).toEqual([
      [
        {
          api_key: '  exact-secret  ',
          site: 'us1',
          account: 'primary',
        },
      ],
    ])
  })

  it('drops all prior values when credential metadata changes', async () => {
    const wrapper = mountForm([
      {
        key: 'token',
        label: 'Token',
        input_type: 'text',
        required: true,
        secret: true,
      },
    ])

    await wrapper.get('#credential-token').setValue('must-not-survive')
    await wrapper.setProps({
      fields: [
        {
          key: 'password',
          label: 'Password',
          input_type: 'text',
          required: true,
          secret: true,
        },
      ],
    })

    expect(wrapper.find('#credential-token').exists()).toBe(false)
    expect(wrapper.get<HTMLInputElement>('#credential-password').element.value).toBe('')
    expect(wrapper.text()).not.toContain('must-not-survive')
  })

  it('submits through the visible native button, not only a synthetic form event', async () => {
    const wrapper = mountForm([
      {
        key: 'token',
        label: 'Token',
        input_type: 'text',
        required: true,
        secret: true,
      },
    ])

    await wrapper.get('#credential-token').setValue('exact-token')
    await wrapper.get('button[type="button"]').trigger('click')

    expect(wrapper.emitted('submit')).toEqual([[{ token: 'exact-token' }]])
  })

  it('allows an explicitly empty Definition-driven authorization field set', async () => {
    const wrapper = mount(CredentialFieldsForm, {
      props: {
        fields: [],
        idPrefix: 'authorization',
        submitLabel: 'Authorize',
        allowEmpty: true,
      },
      global: { plugins: [i18n] },
    })

    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('submit')).toEqual([[{}]])
  })
})
