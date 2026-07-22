<script setup lang="ts">
import { Button, toast } from '@felinic/ui'
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import {
  deleteConfig,
  getConfig,
  getConfigSchema,
  listConnectors,
  putConfig,
  validateConfig,
  verifyMcp,
} from '../api/endpoints'
import type { CatalogItem, ConfigField, ConnectorConfig } from '../api/types'
import ArmButton from '../components/ArmButton.vue'
import ConfigForm, { type ConfigPayload } from '../components/ConfigForm.vue'
import PageShell from '../components/PageShell.vue'
import SettingsRow from '../components/SettingsRow.vue'
import SettingsSection from '../components/SettingsSection.vue'
import StatusBadge from '../components/StatusBadge.vue'

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
    toast.error(e instanceof ApiError ? e.message : '加载配置失败')
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
    toast.success('配置已保存')
  } catch (e) {
    reportError(e, '保存失败')
  } finally {
    busy.value = false
  }
}

async function onValidate(payload: ConfigPayload) {
  try {
    await validateConfig(type.value, payload)
    toast.success('校验通过')
  } catch (e) {
    reportError(e, '校验失败')
  }
}

async function onVerify() {
  busy.value = true
  try {
    await verifyMcp(type.value)
    await reload()
    toast.success('MCP 验证通过')
  } catch (e) {
    reportError(e, 'MCP 验证失败')
  } finally {
    busy.value = false
  }
}

async function onDelete() {
  busy.value = true
  try {
    await deleteConfig(type.value)
    await reload()
    toast.success('配置已删除')
  } catch (e) {
    reportError(e, '删除失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageShell :title="item?.name || type">
    <SettingsSection title="状态">
      <SettingsRow label="当前状态" description="由代码定义与配置实时计算">
        <StatusBadge :status="item?.status ?? ''" />
      </SettingsRow>
      <SettingsRow label="Remote MCP 验证" description="实测握手并比对工具映射；自托管地址修改后需重新验证">
        <Button size="sm" variant="outline" :disabled="busy" @click="onVerify">验证 MCP</Button>
      </SettingsRow>
      <SettingsRow v-if="config" label="删除配置" description="删除后回到待配置状态，已有连接不受影响">
        <ArmButton label="删除配置" :disabled="busy" @confirm="onDelete" />
      </SettingsRow>
    </SettingsSection>

    <SettingsSection title="平台配置">
      <ConfigForm
        v-if="fields.length > 0"
        :fields="fields"
        :config="config"
        :busy="busy"
        @save="onSave"
        @validate="onValidate"
      />
      <SettingsRow v-else label="该连接器没有需要管理员填写的配置" />
    </SettingsSection>
  </PageShell>
</template>
