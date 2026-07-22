<script setup lang="ts">
import { TextButton, toast } from '@felinic/ui'
import { ChevronRight } from 'lucide-vue-next'
import { onMounted, ref } from 'vue'

import { ApiError } from '../api/client'
import { listConnectors } from '../api/endpoints'
import type { CatalogItem } from '../api/types'
import PageShell from '../components/PageShell.vue'
import SettingsRow from '../components/SettingsRow.vue'
import SettingsSection from '../components/SettingsSection.vue'
import StatusBadge from '../components/StatusBadge.vue'

const items = ref<CatalogItem[]>([])
const loading = ref(true)

onMounted(async () => {
  try {
    items.value = await listConnectors()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '加载连接器失败')
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <PageShell title="连接器">
    <SettingsSection>
      <SettingsRow v-for="item in items" :key="item.type">
        <template #label>
          <div class="flex items-center gap-2">
            <span class="text-control font-medium">{{ item.name || item.type }}</span>
            <StatusBadge :status="item.status ?? ''" />
          </div>
          <div class="mt-0.5 text-body text-muted-foreground">
            {{ item.description || item.type }}
          </div>
        </template>
        <TextButton as-child>
          <RouterLink :to="`/connectors/${item.type}`">配置<ChevronRight /></RouterLink>
        </TextButton>
      </SettingsRow>
      <SettingsRow v-if="!loading && items.length === 0" label="没有已注册的连接器" />
    </SettingsSection>
  </PageShell>
</template>
