<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ConnectorStatus } from '../api/types'

// 状态徽标：颜色走 accent 色板 token，文案走 i18n（status.*）。
const props = defineProps<{ status: ConnectorStatus | string }>()

const { t, te } = useI18n()

const hue = computed(() => {
  switch (props.status) {
    case 'ready':
    case 'active':
      return 'green'
    case 'needs_config':
    case 'reauth_required':
    case 'authorization_failed':
      return 'orange'
    case 'config_incompatible':
    case 'definition_missing':
      return 'red'
    default:
      return 'gray'
  }
})

const label = computed(() =>
  te(`status.${props.status}`) ? t(`status.${props.status}`) : String(props.status),
)
</script>

<template>
  <span
    class="inline-flex items-center rounded-sm px-1.5 py-0.5 text-caption font-medium"
    :style="{
      color: `var(--accent-${hue})`,
      backgroundColor: `var(--accent-${hue}-soft)`,
    }"
  >
    {{ label }}
  </span>
</template>
