<script setup lang="ts">
import { Button, toast } from '@felinic/ui'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import {
  deleteConfig,
  getConfig,
  getConfigSchema,
  listConnectors,
  putConfig,
} from '../api/endpoints'
import type { CatalogItem, ConfigField, ConnectorConfig } from '../api/types'
import ArmButton from '../components/ArmButton.vue'
import ConfigForm, { type ConfigPayload } from '../components/ConfigForm.vue'
import PageShell from '../components/PageShell.vue'
import SettingsRow from '../components/SettingsRow.vue'
import SettingsSection from '../components/SettingsSection.vue'
import StatusBadge from '../components/StatusBadge.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const type = computed(() => String(route.params.type))

const item = ref<CatalogItem | null>(null)
const fields = ref<ConfigField[]>([])
const config = ref<ConnectorConfig | null>(null)
const busy = ref(false)

async function reload(includeSchema = false) {
  const [connectors, cfg] = await Promise.all([listConnectors(), getConfig(type.value)])
  item.value = connectors.find((c) => c.type === type.value) ?? null
  config.value = cfg
  if (includeSchema) {
    fields.value = await getConfigSchema(type.value)
  }
}

onMounted(async () => {
  try {
    await reload(true)
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('connector.loadFailed'))
    if (e instanceof ApiError && e.status === 404) void router.push('/connectors')
  }
})

function reportError(e: unknown, fallback: string) {
  toast.error(e instanceof ApiError ? e.message : fallback)
}

async function onSave(payload: ConfigPayload) {
  busy.value = true
  try {
    await putConfig(type.value, { ...payload, if_match: config.value?.updated_at })
    await reload()
    toast.success(t('connector.saved'))
  } catch (e) {
    reportError(e, t('connector.saveFailed'))
  } finally {
    busy.value = false
  }
}

async function onDelete() {
  busy.value = true
  try {
    await deleteConfig(type.value)
    await reload()
    toast.success(t('connector.deleted'))
  } catch (e) {
    reportError(e, t('connector.deleteFailed'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageShell :title="item?.name || type" back-to="/connectors">
    <SettingsSection :title="t('connector.statusSection')">
      <SettingsRow :label="t('connector.currentStatus')" :description="t('connector.currentStatusDesc')">
        <StatusBadge :status="item?.status ?? ''" />
      </SettingsRow>
      <SettingsRow v-if="config" :label="t('connector.deleteConfig')" :description="t('connector.deleteConfigDesc')">
        <ArmButton :label="t('connector.deleteConfig')" :disabled="busy" @confirm="onDelete" />
      </SettingsRow>
    </SettingsSection>

    <SettingsSection :title="t('connector.configSection')">
      <ConfigForm
        v-if="fields.length > 0"
        :fields="fields"
        :config="config"
        :busy="busy"
        @save="onSave"
      />
      <SettingsRow v-else :label="t('connector.noFields')" />
    </SettingsSection>
  </PageShell>
</template>
