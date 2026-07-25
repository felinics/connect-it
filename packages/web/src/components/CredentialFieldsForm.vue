<script setup lang="ts">
import { Button, Input, Label, NativeSelect, NativeSelectOption } from '@felinic/ui'
import { reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ConfigField } from '../api/types'

// Connection credentials are deliberately write-only in this form. Values only
// live in component memory while it is mounted; callers unmount/remount the form
// after a successful submit or when the selected auth method changes.
const props = defineProps<{
  fields: ConfigField[]
  idPrefix: string
  submitLabel: string
  busy?: boolean
  allowEmpty?: boolean
  hint?: string
  submitTestId?: string
}>()

const emit = defineEmits<{
  submit: [fields: Record<string, string>]
}>()

const { t } = useI18n()
const values = reactive<Record<string, string>>({})
const errors = reactive<Record<string, string>>({})

function reset() {
  for (const key of Object.keys(values)) delete values[key]
  for (const key of Object.keys(errors)) delete errors[key]
  for (const field of props.fields) {
    const key = field.key ?? ''
    if (key === '') continue
    values[key] = field.secret ? '' : (field.default_value ?? '')
  }
}

watch(() => props.fields, reset, { immediate: true })

function check(): boolean {
  for (const key of Object.keys(errors)) delete errors[key]

  for (const field of props.fields) {
    const key = field.key ?? ''
    if (key === '') continue
    const value = values[key] ?? ''

    if (field.required && value === '') {
      errors[key] = t('connections.fieldRequired')
      continue
    }
    if (value !== '' && field.pattern) {
      try {
        if (!new RegExp(field.pattern).test(value)) {
          errors[key] = t('connections.patternMismatch', { pattern: field.pattern })
        }
      } catch {
        // Definitions are validated server-side. If a Go-compatible pattern
        // cannot be represented by JavaScript, let the server remain authoritative.
      }
    }
  }

  return Object.keys(errors).length === 0
}

function onSubmit() {
  if (!check()) return

  const fields: Record<string, string> = {}
  for (const field of props.fields) {
    const key = field.key ?? ''
    if (key === '') continue
    const value = values[key] ?? ''
    if (value !== '') fields[key] = value
  }
  emit('submit', fields)
}
</script>

<template>
  <form class="space-y-4 p-5" novalidate @submit.prevent="onSubmit">
    <slot name="before" />

    <div v-for="field in props.fields" :key="field.key" class="space-y-1.5">
      <Label :for="`${props.idPrefix}-${field.key}`">
        {{ field.label || field.key }}
        <span v-if="field.required" class="text-destructive"> *</span>
      </Label>
      <NativeSelect
        v-if="field.input_type === 'select'"
        :id="`${props.idPrefix}-${field.key}`"
        v-model="values[field.key ?? '']"
        class="w-full"
        :aria-invalid="errors[field.key ?? ''] ? 'true' : undefined"
        :aria-required="field.required || undefined"
      >
        <NativeSelectOption value="" :disabled="field.required">
          {{ t('connections.selectPlaceholder') }}
        </NativeSelectOption>
        <NativeSelectOption v-for="option in field.options" :key="option" :value="option">
          {{ option }}
        </NativeSelectOption>
      </NativeSelect>
      <Input
        v-else
        :id="`${props.idPrefix}-${field.key}`"
        v-model="values[field.key ?? '']"
        :type="field.secret ? 'password' : field.input_type === 'url' ? 'url' : 'text'"
        :autocomplete="field.secret ? 'new-password' : 'off'"
        :spellcheck="field.secret ? false : undefined"
        :aria-invalid="errors[field.key ?? ''] ? 'true' : undefined"
        :aria-required="field.required || undefined"
      />
      <p v-if="field.description" class="text-body text-muted-foreground">
        {{ field.description }}
      </p>
      <p v-if="errors[field.key ?? '']" class="text-body text-destructive">
        {{ errors[field.key ?? ''] }}
      </p>
    </div>

    <p v-if="props.hint !== ''" class="text-body text-muted-foreground">
      {{ props.hint ?? t('connections.completeCredentialHint') }}
    </p>

    <div class="flex items-center gap-2 pt-1">
      <Button
        type="button"
        :disabled="props.busy || (!props.allowEmpty && props.fields.length === 0)"
        :data-testid="props.submitTestId"
        @click="onSubmit"
      >
        {{ props.submitLabel }}
      </Button>
      <slot name="actions" />
    </div>
  </form>
</template>
