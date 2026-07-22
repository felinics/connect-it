<script setup lang="ts">
import { Button, Input, Label, NativeSelect, NativeSelectOption, toast } from '@felinic/ui'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
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

const { t } = useI18n()
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
    toast.success(t('connections.connected', { alias: connected }))
  } else if (typeof errorCode === 'string' && errorCode !== '') {
    toast.error(t('connections.authError', { code: errorCode }))
  }
  if (connected || errorCode) {
    void router.replace({ path: '/connections' })
  }

  try {
    const [conns, cats] = await Promise.all([listConnections(), listConnectors()])
    connections.value = conns
    connectors.value = cats
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('connections.loadFailed'))
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
      toast.error(e instanceof ApiError ? e.message : t('connections.loadMethodsFailed'))
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
    toast.success(t('connections.created'))
    form.value = { connectorType: '', authMethod: '', alias: '' }
    credentialValues.value = {}
    await reloadConnections()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('connections.createFailed'))
  } finally {
    busy.value = false
  }
}

async function onReauth(conn: Connection) {
  try {
    const { authorization_url } = await reauthConnection(conn.id ?? '')
    if (authorization_url) redirectTo(authorization_url)
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('connections.reauthFailed'))
  }
}

async function onDelete(conn: Connection) {
  try {
    await deleteConnection(conn.id ?? '')
    toast.success(t('connections.deleted', { alias: conn.alias }))
    await reloadConnections()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('connections.deleteFailed'))
  }
}
</script>

<template>
  <PageShell :title="t('connections.title')">
    <SettingsSection :title="t('connections.existing')">
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
        <Button size="sm" variant="outline" @click="onReauth(conn)">{{ t('connections.reauth') }}</Button>
        <ArmButton :label="t('connections.delete')" @confirm="onDelete(conn)" />
      </SettingsRow>
      <SettingsRow v-if="!loading && connections.length === 0" :label="t('connections.empty')" :description="t('connections.emptyDesc')" />
    </SettingsSection>

    <SettingsSection :title="t('connections.create')">
      <form class="space-y-4 p-5" @submit.prevent="submitCreate">
        <div class="space-y-1.5">
          <Label for="conn-type">{{ t('connections.connector') }}</Label>
          <NativeSelect id="conn-type" v-model="form.connectorType" class="w-full">
            <NativeSelectOption value="">{{ t('connections.selectConnector') }}</NativeSelectOption>
            <NativeSelectOption v-for="c in connectors" :key="c.type" :value="c.type ?? ''">
              {{ c.name || c.type }}
            </NativeSelectOption>
          </NativeSelect>
        </div>
        <div v-if="methods.length > 0" class="space-y-1.5">
          <Label for="conn-method">{{ t('connections.authMethod') }}</Label>
          <NativeSelect id="conn-method" v-model="form.authMethod" class="w-full">
            <NativeSelectOption value="">{{ t('connections.selectAuthMethod') }}</NativeSelectOption>
            <NativeSelectOption v-for="m in methods" :key="m.key" :value="m.key ?? ''">
              {{ m.label || m.key }}
            </NativeSelectOption>
          </NativeSelect>
        </div>
        <div class="space-y-1.5">
          <Label for="conn-alias">{{ t('connections.alias') }}</Label>
          <Input id="conn-alias" v-model="form.alias" :placeholder="t('connections.aliasPlaceholder')" />
          <p class="text-body text-muted-foreground">{{ t('connections.aliasHint') }}</p>
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
          {{ selectedMethod?.type === 'oauth2' ? t('connections.startOAuth') : t('connections.submit') }}
        </Button>
      </form>
    </SettingsSection>
  </PageShell>
</template>
