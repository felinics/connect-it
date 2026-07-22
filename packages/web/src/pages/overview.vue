<script setup lang="ts">
import { ActionCard, toast } from '@felinic/ui'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import { listApiTokens, listConnections, listConnectors } from '../api/endpoints'
import type { ApiToken, CatalogItem, Connection } from '../api/types'
import PageShell from '../components/PageShell.vue'
import ProviderLogo from '../components/ProviderLogo.vue'
import StatTile from '../components/StatTile.vue'

const { t } = useI18n()
const router = useRouter()

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
    toast.error(e instanceof ApiError ? e.message : t('overview.loadFailed'))
  } finally {
    loading.value = false
  }
})

const readyCount = computed(() => connectors.value.filter((c) => c.status === 'ready').length)
const activeConnections = computed(
  () => connections.value.filter((c) => c.status === 'active').length,
)
const activeTokens = computed(() => tokens.value.filter((t) => !t.revoked_at).length)

// 需要关注＝真正异常的状态。「待配置」是正常的初始态，可能长期存在大量
// 未启用的连接器，不进清单。
const attentionStatuses = new Set(['degraded', 'config_incompatible', 'definition_missing'])
const attentionConnectors = computed(() =>
  connectors.value.filter((c) => attentionStatuses.has(c.status ?? '')),
)
const attentionConnections = computed(() =>
  connections.value.filter((c) => c.status !== 'active'),
)
const attentionCount = computed(
  () => attentionConnectors.value.length + attentionConnections.value.length,
)
const allGood = computed(() => !loading.value && attentionCount.value === 0)
</script>

<template>
  <PageShell :title="t('overview.title')" wide>
    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <StatTile
        :label="t('overview.connectors')"
        :value="connectors.length"
        :hint="t('overview.readyCount', { n: readyCount })"
      />
      <StatTile
        :label="t('overview.connections')"
        :value="connections.length"
        :hint="t('overview.usableCount', { n: activeConnections })"
      />
      <StatTile :label="t('overview.tokens')" :value="activeTokens" :hint="t('overview.validCount')" />
      <StatTile
        :label="t('overview.attention')"
        :value="attentionCount"
        :hint="allGood ? t('overview.allGood') : t('overview.seeBelow')"
      />
    </div>

    <section v-if="!allGood && !loading" class="space-y-2.5">
      <h2 class="px-2 text-label font-medium text-muted-foreground">{{ t('overview.attention') }}</h2>
      <div class="grid gap-3">
        <ActionCard
          v-for="item in attentionConnectors"
          :key="`c-${item.type}`"
          :title="item.name || item.type || ''"
          :description="`${t(`status.${item.status}`)} · ${t('overview.connectorAbnormal')}`"
          @click="router.push(`/connectors/${item.type}`)"
        >
          <template #icon>
            <ProviderLogo :name="item.name || item.type || ''" :icon-url="item.icon_url" bare />
          </template>
        </ActionCard>
        <ActionCard
          v-for="conn in attentionConnections"
          :key="`n-${conn.id}`"
          :title="conn.alias ?? ''"
          :description="`${conn.connector_type} · ${t('overview.connectionNeedsAction')}`"
          @click="router.push('/connections')"
        >
          <template #icon>
            <ProviderLogo :name="conn.alias ?? '?'" bare />
          </template>
        </ActionCard>
      </div>
    </section>
  </PageShell>
</template>
