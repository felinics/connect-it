<script setup lang="ts">
import { Button, Input, Label, NativeSelect, NativeSelectOption, toast } from '@felinic/ui'
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import {
  createApiKeyConnection,
  deleteConnection,
  listAuthMethods,
  listConnections,
  listConnectors,
  reauthConnection,
  startOAuth,
} from '../api/endpoints'
import type { AuthMethod, CatalogItem, Connection } from '../api/types'
import { redirectTo } from '../lib/navigation'
import ArmButton from '../components/ArmButton.vue'
import PageShell from '../components/PageShell.vue'
import SettingsRow from '../components/SettingsRow.vue'
import SettingsSection from '../components/SettingsSection.vue'
import StatusBadge from '../components/StatusBadge.vue'

const route = useRoute()
const router = useRouter()

const connections = ref<Connection[]>([])
const connectors = ref<CatalogItem[]>([])
const methods = ref<AuthMethod[]>([])
const loading = ref(true)
const busy = ref(false)

const form = ref({ connectorType: '', authMethod: '', alias: '' })
const credentialValues = ref<Record<string, string>>({})

const selectedMethod = computed(
  () => methods.value.find((m) => m.key === form.value.authMethod) ?? null,
)

async function reloadConnections() {
  connections.value = await listConnections()
}

onMounted(async () => {
  // OAuth 回调带回的结果提示（?connected= / ?error=），提示后清掉 query。
  const connected = route.query.connected
  const errorCode = route.query.error
  if (typeof connected === 'string' && connected !== '') {
    toast.success(`连接 ${connected} 授权成功`)
  } else if (typeof errorCode === 'string' && errorCode !== '') {
    toast.error(`授权失败：${errorCode}`)
  }
  if (connected || errorCode) {
    void router.replace({ path: '/connections' })
  }

  try {
    const [conns, cats] = await Promise.all([listConnections(), listConnectors()])
    connections.value = conns
    connectors.value = cats
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '加载连接失败')
  } finally {
    loading.value = false
  }
})

watch(
  () => form.value.connectorType,
  async (type) => {
    form.value.authMethod = ''
    methods.value = []
    credentialValues.value = {}
    if (!type) return
    try {
      methods.value = await listAuthMethods(type)
      if (methods.value.length === 1) {
        form.value.authMethod = methods.value[0]?.key ?? ''
      }
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : '加载认证方式失败')
    }
  },
)

watch(selectedMethod, () => {
  credentialValues.value = {}
})

async function submitCreate() {
  const method = selectedMethod.value
  if (!method) return
  busy.value = true
  try {
    if (method.type === 'oauth2') {
      const { authorization_url } = await startOAuth({
        connector_type: form.value.connectorType,
        auth_method: method.key ?? '',
        alias: form.value.alias,
      })
      if (authorization_url) redirectTo(authorization_url)
      return
    }
    await createApiKeyConnection({
      connector_type: form.value.connectorType,
      auth_method: method.key ?? '',
      alias: form.value.alias,
      fields: credentialValues.value,
    })
    toast.success('连接已创建')
    form.value = { connectorType: '', authMethod: '', alias: '' }
    credentialValues.value = {}
    await reloadConnections()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '创建连接失败')
  } finally {
    busy.value = false
  }
}

async function onReauth(conn: Connection) {
  try {
    const { authorization_url } = await reauthConnection(conn.id ?? '')
    if (authorization_url) redirectTo(authorization_url)
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '发起重新授权失败')
  }
}

async function onDelete(conn: Connection) {
  try {
    await deleteConnection(conn.id ?? '')
    toast.success(`连接 ${conn.alias} 已删除`)
    await reloadConnections()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '删除连接失败')
  }
}
</script>

<template>
  <PageShell title="连接">
    <SettingsSection title="已有连接">
      <SettingsRow v-for="conn in connections" :key="conn.id">
        <template #label>
          <div class="flex items-center gap-2">
            <span class="text-control font-medium">{{ conn.alias }}</span>
            <StatusBadge :status="conn.status ?? ''" />
          </div>
          <div class="mt-0.5 text-body text-muted-foreground">
            {{ conn.connector_type }} · {{ conn.auth_method }}
          </div>
        </template>
        <Button size="sm" variant="outline" @click="onReauth(conn)">重新授权</Button>
        <ArmButton label="删除" @confirm="onDelete(conn)" />
      </SettingsRow>
      <SettingsRow v-if="!loading && connections.length === 0" label="还没有任何连接" description="在下方创建第一个连接" />
    </SettingsSection>

    <SettingsSection title="新建连接">
      <form class="space-y-4 p-5" @submit.prevent="submitCreate">
        <div class="space-y-1.5">
          <Label for="conn-type">连接器</Label>
          <NativeSelect id="conn-type" v-model="form.connectorType" class="w-full">
            <NativeSelectOption value="">选择连接器</NativeSelectOption>
            <NativeSelectOption v-for="c in connectors" :key="c.type" :value="c.type ?? ''">
              {{ c.name || c.type }}
            </NativeSelectOption>
          </NativeSelect>
        </div>
        <div v-if="methods.length > 0" class="space-y-1.5">
          <Label for="conn-method">认证方式</Label>
          <NativeSelect id="conn-method" v-model="form.authMethod" class="w-full">
            <NativeSelectOption value="">选择认证方式</NativeSelectOption>
            <NativeSelectOption v-for="m in methods" :key="m.key" :value="m.key ?? ''">
              {{ m.label || m.key }}
            </NativeSelectOption>
          </NativeSelect>
        </div>
        <div class="space-y-1.5">
          <Label for="conn-alias">别名</Label>
          <Input id="conn-alias" v-model="form.alias" placeholder="如 gh-main（小写字母、数字、连字符）" />
          <p class="text-body text-muted-foreground">别名用于在聚合 MCP 中标识这个连接，创建后不可改。</p>
        </div>
        <div v-if="selectedMethod && selectedMethod.type !== 'oauth2'" class="space-y-4">
          <div v-for="f in selectedMethod.credential_fields" :key="f.key" class="space-y-1.5">
            <Label :for="`cred-${f.key}`">
              {{ f.label || f.key }}<span v-if="f.required" class="text-destructive"> *</span>
            </Label>
            <Input
              :id="`cred-${f.key}`"
              v-model="credentialValues[f.key ?? '']"
              :type="f.secret ? 'password' : 'text'"
              autocomplete="off"
            />
            <p v-if="f.description" class="text-body text-muted-foreground">{{ f.description }}</p>
          </div>
        </div>
        <Button
          type="submit"
          :disabled="busy || !form.connectorType || !form.authMethod || !form.alias"
        >
          {{ selectedMethod?.type === 'oauth2' ? '发起授权' : '创建连接' }}
        </Button>
      </form>
    </SettingsSection>
  </PageShell>
</template>
