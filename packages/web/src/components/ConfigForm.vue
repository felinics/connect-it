<script setup lang="ts">
import { Button, Input, Label, NativeSelect, NativeSelectOption } from '@felinic/ui'
import { computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ConfigField, ConnectorConfig } from '../api/types'

export interface ConfigPayload {
  public: Record<string, unknown>
  secrets: Record<string, string>
}

// 由 config-schema 元数据驱动的动态表单：Secret 只写不读（已设置显示占位），
// 校验在提交时进行（规范：表单提交时才校验）。
const props = defineProps<{
  fields: ConfigField[]
  config: ConnectorConfig | null
  busy?: boolean
}>()
const emit = defineEmits<{
  save: [payload: ConfigPayload]
  validate: [payload: ConfigPayload]
}>()

const { t } = useI18n()

const values = reactive<Record<string, string>>({})
const errors = reactive<Record<string, string>>({})

const secretSet = computed(() => new Set(props.config?.secret_keys_set ?? []))

watch(
  () => [props.fields, props.config] as const,
  () => {
    for (const f of props.fields) {
      const key = f.key ?? ''
      if (f.secret) {
        values[key] = ''
        continue
      }
      const fromConfig = props.config?.public?.[key]
      values[key] = typeof fromConfig === 'string' ? fromConfig : (f.default_value ?? '')
    }
  },
  { immediate: true, deep: false },
)

function check(): boolean {
  for (const key of Object.keys(errors)) delete errors[key]
  for (const f of props.fields) {
    const key = f.key ?? ''
    const value = (values[key] ?? '').trim()
    if (f.required) {
      const satisfied = f.secret ? value !== '' || secretSet.value.has(key) : value !== ''
      if (!satisfied) {
        errors[key] = t('connector.fieldRequired')
        continue
      }
    }
    if (value !== '' && f.pattern) {
      try {
        if (!new RegExp(f.pattern).test(value)) {
          errors[key] = t('connector.patternMismatch', { pattern: f.pattern })
        }
      } catch {
        // 非法正则交给服务端报错
      }
    }
  }
  return Object.keys(errors).length === 0
}

function buildPayload(): ConfigPayload {
  const pub: Record<string, unknown> = {}
  const secrets: Record<string, string> = {}
  for (const f of props.fields) {
    const key = f.key ?? ''
    const value = (values[key] ?? '').trim()
    if (value === '') continue
    if (f.secret) {
      secrets[key] = value
    } else {
      pub[key] = value
    }
  }
  return { public: pub, secrets }
}

function onSave() {
  if (!check()) return
  emit('save', buildPayload())
}

function onValidate() {
  if (!check()) return
  emit('validate', buildPayload())
}
</script>

<template>
  <form class="space-y-4 p-5" @submit.prevent="onSave">
    <div v-for="f in props.fields" :key="f.key" class="space-y-1.5">
      <Label :for="`cfg-${f.key}`">
        {{ f.label || f.key }}<span v-if="f.required" class="text-destructive"> *</span>
      </Label>
      <NativeSelect
        v-if="f.input_type === 'select'"
        :id="`cfg-${f.key}`"
        v-model="values[f.key ?? '']"
        class="w-full"
      >
        <NativeSelectOption v-if="!f.required" value="">—</NativeSelectOption>
        <NativeSelectOption v-for="opt in f.options" :key="opt" :value="opt">{{ opt }}</NativeSelectOption>
      </NativeSelect>
      <Input
        v-else
        :id="`cfg-${f.key}`"
        v-model="values[f.key ?? '']"
        :type="f.secret ? 'password' : 'text'"
        :placeholder="f.secret && secretSet.has(f.key ?? '') ? t('connector.secretSet') : undefined"
        autocomplete="off"
      />
      <p v-if="f.description" class="text-body text-muted-foreground">{{ f.description }}</p>
      <p v-if="errors[f.key ?? '']" class="text-body text-destructive">{{ errors[f.key ?? ''] }}</p>
    </div>

    <div class="flex items-center gap-2 pt-1">
      <Button type="submit" :disabled="props.busy">{{ t('connector.save') }}</Button>
      <Button type="button" variant="outline" :disabled="props.busy" @click="onValidate">{{ t('connector.validate') }}</Button>
      <slot name="extra" />
    </div>
  </form>
</template>
