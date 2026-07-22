<script setup lang="ts">
import { computed } from 'vue'

import type { ConnectorStatus } from '../api/types'

// 七态徽标：颜色一律走 accent 色板 token（AGENTS.md「Accent palette」），
// 文案直白、不出现内部缩写。
const props = defineProps<{ status: ConnectorStatus | string }>()

const meta = computed(() => {
  switch (props.status) {
    case 'ready':
      return { label: '就绪', hue: 'green' }
    case 'active':
      return { label: '已连接', hue: 'green' }
    case 'needs_config':
      return { label: '待配置', hue: 'orange' }
    case 'reauth_required':
      return { label: '需重新授权', hue: 'orange' }
    case 'catalog_only':
      return { label: '仅目录', hue: 'gray' }
    case 'degraded':
      return { label: '异常', hue: 'red' }
    case 'config_incompatible':
      return { label: '配置版本不兼容', hue: 'red' }
    case 'deprecated':
      return { label: '已弃用', hue: 'gray' }
    case 'definition_missing':
      return { label: '定义缺失', hue: 'red' }
    case 'disabled':
      return { label: '已停用', hue: 'gray' }
    default:
      return { label: String(props.status), hue: 'gray' }
  }
})
</script>

<template>
  <span
    class="inline-flex items-center rounded-sm px-1.5 py-0.5 text-caption font-medium"
    :style="{
      color: `var(--accent-${meta.hue})`,
      backgroundColor: `var(--accent-${meta.hue}-soft)`,
    }"
  >
    {{ meta.label }}
  </span>
</template>
