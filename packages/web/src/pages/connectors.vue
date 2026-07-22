<script setup lang="ts">
import { Input, SegmentedControl, TextButton, toast } from '@felinic/ui'
import { ChevronRight } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import { ApiError } from '../api/client'
import { listConnectors } from '../api/endpoints'
import type { CatalogItem } from '../api/types'
import PageShell from '../components/PageShell.vue'
import ProviderLogo from '../components/ProviderLogo.vue'
import SettingsSection from '../components/SettingsSection.vue'
import StatusBadge from '../components/StatusBadge.vue'

type Filter = 'all' | 'ready' | 'needs_config' | 'other'

const items = ref<CatalogItem[]>([])
const loading = ref(true)
const query = ref('')
const filter = ref<Filter>('all')

onMounted(async () => {
  try {
    items.value = await listConnectors()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '加载连接器失败')
  } finally {
    loading.value = false
  }
})

function inFilter(item: CatalogItem, f: Filter): boolean {
  switch (f) {
    case 'all':
      return true
    case 'ready':
      return item.status === 'ready'
    case 'needs_config':
      return item.status === 'needs_config'
    case 'other':
      return item.status !== 'ready' && item.status !== 'needs_config'
  }
}

const counts = computed(() => ({
  all: items.value.length,
  ready: items.value.filter((i) => inFilter(i, 'ready')).length,
  needs_config: items.value.filter((i) => inFilter(i, 'needs_config')).length,
  other: items.value.filter((i) => inFilter(i, 'other')).length,
}))

const filterItems = computed(() => [
  { value: 'all' as const, label: `全部 ${counts.value.all}` },
  { value: 'ready' as const, label: `就绪 ${counts.value.ready}` },
  { value: 'needs_config' as const, label: `待配置 ${counts.value.needs_config}` },
  { value: 'other' as const, label: `其他 ${counts.value.other}` },
])

const visible = computed(() => {
  const q = query.value.trim().toLowerCase()
  return items.value.filter((item) => {
    if (!inFilter(item, filter.value)) return false
    if (q === '') return true
    return [item.name, item.type, item.description]
      .filter((s): s is string => typeof s === 'string')
      .some((s) => s.toLowerCase().includes(q))
  })
})
</script>

<template>
  <PageShell title="连接器" wide>
    <SettingsSection>
      <!-- 列表头：数量、搜索与状态筛选 -->
      <div class="flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-4">
        <div class="flex items-center gap-3">
          <span class="text-control font-medium">全部连接器</span>
          <span class="text-body text-muted-foreground">显示 {{ visible.length }} / {{ items.length }}</span>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <SegmentedControl v-model="filter" :items="filterItems" aria-label="按状态筛选" />
          <Input v-model="query" class="w-56" placeholder="搜索连接器" />
        </div>
      </div>

      <div
        v-for="item in visible"
        :key="item.type"
        class="mx-4 flex items-center gap-4 border-b border-border py-4 last:border-b-0"
      >
        <ProviderLogo :name="item.name || item.type || ''" :icon-url="item.icon_url" />
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <span class="truncate text-title font-medium">{{ item.name || item.type }}</span>
            <StatusBadge :status="item.status ?? ''" />
          </div>
          <div class="mt-0.5 truncate text-label text-muted-foreground">
            {{ item.description || item.type }}
          </div>
        </div>
        <TextButton as-child>
          <RouterLink :to="`/connectors/${item.type}`">配置<ChevronRight /></RouterLink>
        </TextButton>
      </div>

      <div v-if="!loading && visible.length === 0" class="px-5 py-10 text-center text-body text-muted-foreground">
        {{ items.length === 0 ? '没有已注册的连接器' : '没有符合条件的连接器' }}
      </div>
    </SettingsSection>
  </PageShell>
</template>
