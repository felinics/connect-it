<script setup lang="ts">
import { TextButton, toast } from '@felinic/ui'
import { ChevronRight } from 'lucide-vue-next'
import { computed, onMounted, ref } from 'vue'

import { ApiError } from '../api/client'
import { listApiTokens, listConnections, listConnectors } from '../api/endpoints'
import type { ApiToken, CatalogItem, Connection } from '../api/types'
import PageShell from '../components/PageShell.vue'
import ProviderLogo from '../components/ProviderLogo.vue'
import SettingsRow from '../components/SettingsRow.vue'
import SettingsSection from '../components/SettingsSection.vue'
import StatTile from '../components/StatTile.vue'
import StatusBadge from '../components/StatusBadge.vue'

const connectors = ref<CatalogItem[]>([])
const connections = ref<Connection[]>([])
const tokens = ref<ApiToken[]>([])
const loading = ref(true)

onMounted(async () => {
  try {
    ;[connectors.value, connections.value, tokens.value] = await Promise.all([
      listConnectors(),
      listConnections(),
      listApiTokens(),
    ])
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '加载概览失败')
  } finally {
    loading.value = false
  }
})

const readyCount = computed(() => connectors.value.filter((c) => c.status === 'ready').length)
const activeConnections = computed(
  () => connections.value.filter((c) => c.status === 'active').length,
)
const activeTokens = computed(() => tokens.value.filter((t) => !t.revoked_at).length)

// 需要关注：未就绪的连接器＋需要重新授权的连接。首页只回答「现在是否正常」。
const attentionConnectors = computed(() =>
  connectors.value.filter((c) => c.status !== 'ready' && c.status !== 'catalog_only'),
)
const attentionConnections = computed(() =>
  connections.value.filter((c) => c.status !== 'active'),
)
const allGood = computed(
  () =>
    !loading.value &&
    attentionConnectors.value.length === 0 &&
    attentionConnections.value.length === 0,
)
</script>

<template>
  <PageShell title="概览" wide>
    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <StatTile label="连接器" :value="connectors.length" :hint="`${readyCount} 个就绪`" />
      <StatTile label="连接" :value="connections.length" :hint="`${activeConnections} 个可用`" />
      <StatTile label="API Token" :value="activeTokens" hint="有效数量" />
      <StatTile
        label="需要关注"
        :value="attentionConnectors.length + attentionConnections.length"
        :hint="allGood ? '一切正常' : '见下方清单'"
      />
    </div>

    <SettingsSection v-if="!allGood && !loading" title="需要关注">
      <SettingsRow v-for="item in attentionConnectors" :key="`c-${item.type}`">
        <template #label>
          <div class="flex items-center gap-3">
            <ProviderLogo :name="item.name || item.type || ''" :icon-url="item.icon_url" />
            <div>
              <div class="flex items-center gap-2">
                <span class="text-control font-medium">{{ item.name || item.type }}</span>
                <StatusBadge :status="item.status ?? ''" />
              </div>
              <div class="mt-0.5 text-body text-muted-foreground">连接器未就绪</div>
            </div>
          </div>
        </template>
        <TextButton as-child>
          <RouterLink :to="`/connectors/${item.type}`">去配置<ChevronRight /></RouterLink>
        </TextButton>
      </SettingsRow>
      <SettingsRow v-for="conn in attentionConnections" :key="`n-${conn.id}`">
        <template #label>
          <div class="flex items-center gap-2">
            <span class="text-control font-medium">{{ conn.alias }}</span>
            <StatusBadge :status="conn.status ?? ''" />
          </div>
          <div class="mt-0.5 text-body text-muted-foreground">
            {{ conn.connector_type }} 连接需要处理
          </div>
        </template>
        <TextButton as-child>
          <RouterLink to="/connections">去处理<ChevronRight /></RouterLink>
        </TextButton>
      </SettingsRow>
    </SettingsSection>
  </PageShell>
</template>
