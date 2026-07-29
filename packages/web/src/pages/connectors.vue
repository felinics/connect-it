<script setup lang="ts">
import { ActionCard, Input, SegmentedControl, toast } from '@felinic/ui'
import { ChevronRightIcon } from '@radix-icons/vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { ApiError } from '../api/client'
import { listConnectors } from '../api/endpoints'
import type { CatalogItem } from '../api/types'
import PageShell from '../components/PageShell.vue'
import ProviderLogo from '../components/ProviderLogo.vue'
import StatusBadge from '../components/StatusBadge.vue'

type Filter = 'all' | 'ready' | 'needs_config' | 'other'

const { t } = useI18n()

const items = ref<CatalogItem[]>([])
const loading = ref(true)
const query = ref('')
const filter = ref<Filter>('all')

onMounted(async () => {
  try {
    items.value = await listConnectors()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('connectors.loadFailed'))
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

const filterItems = computed(() => [
  { value: 'all' as const, label: t('connectors.filterAll', { n: items.value.length }) },
  {
    value: 'ready' as const,
    label: t('connectors.filterReady', { n: items.value.filter((i) => inFilter(i, 'ready')).length }),
  },
  {
    value: 'needs_config' as const,
    label: t('connectors.filterNeedsConfig', {
      n: items.value.filter((i) => inFilter(i, 'needs_config')).length,
    }),
  },
  {
    value: 'other' as const,
    label: t('connectors.filterOther', { n: items.value.filter((i) => inFilter(i, 'other')).length }),
  },
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
  <PageShell :title="t('connectors.title')" wide>
    <!-- Toolbar: status filter and search -->
    <div class="flex flex-wrap items-center justify-between gap-3 px-2">
      <div class="flex items-center gap-3">
        <span class="text-body text-muted-foreground">
          {{ t('connectors.showing', { shown: visible.length, total: items.length }) }}
        </span>
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <SegmentedControl v-model="filter" :items="filterItems" :aria-label="t('connectors.title')" />
        <Input v-model="query" class="w-56" :placeholder="t('connectors.searchPlaceholder')" />
      </div>
    </div>

    <!-- Connector grid where the whole card is clickable -->
    <div class="grid gap-4 sm:grid-cols-2">
      <ActionCard
        v-for="item in visible"
        :key="item.type"
        :title="item.name || item.type || ''"
        :description="item.description || item.type"
        @click="$router.push(`/connectors/${item.type}`)"
      >
        <template #icon>
          <ProviderLogo :name="item.name || item.type || ''" :icon-url="item.icon_url" bare />
        </template>
        <template #trailing>
          <div class="flex shrink-0 items-center gap-2">
            <StatusBadge :status="item.status ?? ''" />
            <ChevronRightIcon class="size-4 text-muted-foreground" />
          </div>
        </template>
      </ActionCard>
    </div>

    <div
      v-if="!loading && visible.length === 0"
      class="px-2 py-10 text-center text-body text-muted-foreground"
    >
      {{ items.length === 0 ? t('connectors.empty') : t('connectors.noMatch') }}
    </div>
  </PageShell>
</template>
