<script setup lang="ts">
import { Button, Input, Label, NativeSelect, NativeSelectOption, toast } from '@felinic/ui'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { ApiError } from '../api/client'
import {
  adminBeginOAuthConnection,
  adminCreateApiKeyConnection,
  adminDeleteConnection,
  adminReauthConnection,
  adminRecredentialConnection,
  listAuthMethods,
  listConnections,
  listConnectors,
} from '../api/endpoints'
import type { AuthMethod, CatalogItem, ConfigField, Connection } from '../api/types'
import ArmButton from '../components/ArmButton.vue'
import CredentialFieldsForm from '../components/CredentialFieldsForm.vue'
import PageShell from '../components/PageShell.vue'
import SettingsRow from '../components/SettingsRow.vue'
import SettingsSection from '../components/SettingsSection.vue'
import StatusBadge from '../components/StatusBadge.vue'

const { t } = useI18n()

const connections = ref<Connection[]>([])
const catalog = ref<CatalogItem[]>([])
const methodsByConnector = ref<Record<string, AuthMethod[]>>({})
const loading = ref(true)
const query = ref('')

const selectedConnector = ref('')
const selectedAuthMethod = ref('')
const alias = ref('')
const aliasError = ref('')
const createBusy = ref(false)
const createFormVersion = ref(0)

const editingConnection = ref<Connection | null>(null)
const connectionBusyID = ref('')

interface OAuthLink {
  url: string
  connectorLabel: string
}

const oauthLink = ref<OAuthLink | null>(null)

function reportError(error: unknown, fallback: string) {
  toast.error(error instanceof ApiError ? error.message : fallback)
}

async function reloadConnections() {
  connections.value = await listConnections()
}

function isSupportedAuthMethod(method: AuthMethod): boolean {
  return (
    method.type === 'oauth2' ||
    method.type === 'api_key' ||
    method.type === 'custom_credential'
  )
}

async function ensureAuthMethods(connectorType: string) {
  if (Object.prototype.hasOwnProperty.call(methodsByConnector.value, connectorType)) return
  const methods = await listAuthMethods(connectorType)
  methodsByConnector.value = {
    ...methodsByConnector.value,
    [connectorType]: methods.filter(isSupportedAuthMethod),
  }
}

onMounted(async () => {
  try {
    const [connectorItems, currentConnections] = await Promise.all([
      listConnectors(),
      listConnections(),
    ])
    catalog.value = [...connectorItems].sort((a, b) =>
      (a.name ?? a.type ?? '').localeCompare(b.name ?? b.type ?? ''),
    )
    connections.value = currentConnections

    const connectorTypes = new Set<string>()
    for (const item of connectorItems) {
      if (item.type) connectorTypes.add(item.type)
    }
    for (const connection of currentConnections) {
      if (connection.connector_type) connectorTypes.add(connection.connector_type)
    }
    const methodLoads = await Promise.allSettled(
      [...connectorTypes].map((connectorType) => ensureAuthMethods(connectorType)),
    )
    if (methodLoads.some((result) => result.status === 'rejected')) {
      toast.error(t('connections.methodsLoadFailed'))
    }

    const first = catalog.value.find((item) =>
      (methodsByConnector.value[item.type ?? ''] ?? []).length > 0,
    )
    selectedConnector.value = first?.type ?? ''
  } catch (error) {
    reportError(error, t('connections.loadFailed'))
  } finally {
    loading.value = false
  }
})

const createableConnectors = computed(() =>
  catalog.value.filter((item) => (methodsByConnector.value[item.type ?? ''] ?? []).length > 0),
)

const selectedMethods = computed(() => methodsByConnector.value[selectedConnector.value] ?? [])

const selectedMethod = computed(
  () => selectedMethods.value.find((method) => method.key === selectedAuthMethod.value) ?? null,
)

const isOAuthCreation = computed(() => selectedMethod.value?.type === 'oauth2')
const isCredentialCreation = computed(
  () =>
    selectedMethod.value?.type === 'api_key' ||
    selectedMethod.value?.type === 'custom_credential',
)

watch(selectedConnector, async (connectorType) => {
  selectedAuthMethod.value = ''
  createFormVersion.value += 1
  if (connectorType === '') return
  try {
    await ensureAuthMethods(connectorType)
    selectedAuthMethod.value = (methodsByConnector.value[connectorType] ?? [])[0]?.key ?? ''
  } catch (error) {
    reportError(error, t('connections.methodsLoadFailed'))
  }
})

watch(selectedAuthMethod, () => {
  createFormVersion.value += 1
})

watch(alias, () => {
  aliasError.value = ''
})

const visible = computed(() => {
  const normalizedQuery = query.value.trim().toLowerCase()
  if (normalizedQuery === '') return connections.value
  return connections.value.filter((connection) =>
    [
      connection.alias,
      connection.connector_type,
      connection.id,
      connection.auth_method,
    ]
      .filter((value): value is string => typeof value === 'string')
      .some((value) => value.toLowerCase().includes(normalizedQuery)),
  )
})

function authMethodFor(connection: Connection): AuthMethod | null {
  return (
    (methodsByConnector.value[connection.connector_type ?? ''] ?? []).find(
      (method) => method.key === connection.auth_method,
    ) ?? null
  )
}

function isAuthorizationConnection(connection: Connection): boolean {
  return authMethodFor(connection)?.type === 'oauth2'
}

function isCredentialConnection(connection: Connection): boolean {
  const type = authMethodFor(connection)?.type
  return type === 'api_key' || type === 'custom_credential'
}

function connectorLabel(connectorType: string): string {
  return (
    catalog.value.find((item) => item.type === connectorType)?.name ||
    connectorType ||
    t('connections.unknownConnector')
  )
}

function optionalAlias(): string | undefined {
  const value = alias.value.trim()
  return value === '' ? undefined : value
}

function validateAlias(): boolean {
  const value = alias.value.trim()
  if (value === '' || /^[a-z0-9][a-z0-9-]{0,31}$/.test(value)) {
    aliasError.value = ''
    return true
  }
  aliasError.value = t('connections.aliasInvalid')
  return false
}

function safeAuthorizationURL(value: string | undefined): string | null {
  if (!value) return null
  try {
    const parsed = new URL(value)
    return parsed.protocol === 'https:' && parsed.username === '' && parsed.password === ''
      ? parsed.href
      : null
  } catch {
    return null
  }
}

function rememberOAuthLink(
  result: { authorization_url?: string; connection_id?: string },
  connectorType: string,
): boolean {
  const url = safeAuthorizationURL(result.authorization_url)
  if (!url) {
    toast.error(t('connections.invalidOAuthUrl'))
    return false
  }
  oauthLink.value = {
    url,
    connectorLabel: connectorLabel(connectorType),
  }
  return true
}

// OAuth 与凭证两种创建方式只有「调哪个接口」不同，其余（别名校验、忙碌态、
// 重置表单、刷新列表）完全一致，所以共用一个提交入口。
const createForm = computed(() => {
  const oauth = isOAuthCreation.value
  return {
    fields: oauth ? ([] as ConfigField[]) : (selectedMethod.value?.credential_fields ?? []),
    submitLabel: t(oauth ? 'connections.startOAuth' : 'connections.create'),
    submitTestId: oauth ? 'start-oauth' : 'create-credential',
    testId: oauth ? 'create-oauth-form' : 'create-credential-form',
  }
})

async function submitCreation(fields: Record<string, string>) {
  if (
    (!isOAuthCreation.value && !isCredentialCreation.value) ||
    selectedConnector.value === '' ||
    selectedAuthMethod.value === ''
  ) {
    return
  }
  if (!validateAlias()) return
  createBusy.value = true
  try {
    const request = {
      connector_type: selectedConnector.value,
      auth_method: selectedAuthMethod.value,
      alias: optionalAlias(),
    }
    let success = t('connections.created')
    if (isOAuthCreation.value) {
      const result = await adminBeginOAuthConnection(request)
      success = rememberOAuthLink(result, selectedConnector.value)
        ? t('connections.oauthReady')
        : ''
    } else {
      await adminCreateApiKeyConnection({ ...request, fields })
    }
    alias.value = ''
    createFormVersion.value += 1
    await reloadConnections()
    if (success !== '') toast.success(success)
  } catch (error) {
    reportError(error, t('connections.createFailed'))
  } finally {
    createBusy.value = false
  }
}

async function beginReauth(connection: Connection) {
  if (!isAuthorizationConnection(connection) || !connection.id) return
  connectionBusyID.value = connection.id
  try {
    const result = await adminReauthConnection(connection.id)
    if (rememberOAuthLink(result, connection.connector_type ?? '')) {
      toast.success(t('connections.reauthReady'))
    }
  } catch (error) {
    reportError(error, t('connections.reauthFailed'))
  } finally {
    connectionBusyID.value = ''
  }
}

function startCredentialReplacement(connection: Connection) {
  if (!isCredentialConnection(connection)) return
  editingConnection.value = connection
}

async function replaceCredentials(fields: Record<string, string>) {
  const connection = editingConnection.value
  if (!connection?.id || !isCredentialConnection(connection)) return
  connectionBusyID.value = connection.id
  try {
    await adminRecredentialConnection(connection.id, fields)
    editingConnection.value = null
    await reloadConnections()
    toast.success(t('connections.replaced'))
  } catch (error) {
    reportError(error, t('connections.replaceFailed'))
  } finally {
    connectionBusyID.value = ''
  }
}

async function copyOAuthLink() {
  if (!oauthLink.value) return
  try {
    await navigator.clipboard.writeText(oauthLink.value.url)
    toast.success(t('connections.oauthCopied'))
  } catch {
    toast.error(t('connections.oauthCopyFailed'))
  }
}

function openOAuthLink() {
  if (!oauthLink.value) return
  // noreferrer implies noopener in modern browsers; setting opener again is a
  // defense for older implementations that still return a WindowProxy.
  const opened = window.open(oauthLink.value.url, '_blank', 'noopener,noreferrer')
  if (opened) opened.opener = null
}

async function onDelete(connection: Connection) {
  if (!connection.id) return
  connectionBusyID.value = connection.id
  try {
    await adminDeleteConnection(connection.id)
    if (editingConnection.value?.id === connection.id) editingConnection.value = null
    toast.success(t('connections.deleted'))
    await reloadConnections()
  } catch (error) {
    reportError(error, t('connections.deleteFailed'))
  } finally {
    connectionBusyID.value = ''
  }
}

const editingMethod = computed(() =>
  editingConnection.value ? authMethodFor(editingConnection.value) : null,
)

const oauthLinkHost = computed(() => {
  if (!oauthLink.value) return ''
  return new URL(oauthLink.value.url).host
})
</script>

<template>
  <PageShell :title="t('connections.title')" wide>
    <p class="px-2 text-body text-muted-foreground">{{ t('connections.subtitle') }}</p>

    <SettingsSection :title="t('connections.createSection')">
      <CredentialFieldsForm
        v-if="isCredentialCreation || isOAuthCreation"
        :key="`${selectedConnector}:${selectedAuthMethod}:${createFormVersion}`"
        :fields="createForm.fields"
        id-prefix="create-credential"
        :submit-label="createForm.submitLabel"
        :submit-test-id="createForm.submitTestId"
        :busy="createBusy"
        :allow-empty="isOAuthCreation"
        :data-testid="createForm.testId"
        @submit="submitCreation"
      >
        <template #before>
          <div class="space-y-1.5">
            <Label for="create-connector">{{ t('connections.connector') }}</Label>
            <NativeSelect
              id="create-connector"
              v-model="selectedConnector"
              class="w-full"
              data-testid="create-connector"
            >
              <NativeSelectOption
                v-for="connector in createableConnectors"
                :key="connector.type"
                :value="connector.type"
              >
                {{ connector.name || connector.type }}
              </NativeSelectOption>
            </NativeSelect>
          </div>
          <div class="space-y-1.5">
            <Label for="create-auth-method">{{ t('connections.authMethod') }}</Label>
            <NativeSelect
              id="create-auth-method"
              v-model="selectedAuthMethod"
              class="w-full"
              data-testid="create-auth-method"
            >
              <NativeSelectOption
                v-for="method in selectedMethods"
                :key="method.key"
                :value="method.key"
              >
                {{ method.label || method.key }}
              </NativeSelectOption>
            </NativeSelect>
          </div>
          <div class="space-y-1.5">
            <Label for="create-alias">{{ t('connections.alias') }}</Label>
            <Input
              id="create-alias"
              v-model="alias"
              :placeholder="t('connections.aliasPlaceholder')"
              autocomplete="off"
              :aria-invalid="aliasError ? 'true' : undefined"
            />
            <p class="text-body text-muted-foreground">{{ t('connections.aliasHint') }}</p>
            <p v-if="aliasError" class="text-body text-destructive">{{ aliasError }}</p>
          </div>
        </template>
      </CredentialFieldsForm>

      <SettingsRow
        v-else-if="!loading"
        :label="t('connections.noCreateableConnectors')"
        :description="t('connections.noCreateableConnectorsDesc')"
      />
    </SettingsSection>

    <SettingsSection
      v-if="oauthLink"
      :title="t('connections.oauthLinkSection')"
      data-testid="oauth-link-section"
    >
      <SettingsRow
        :label="t('connections.oauthLinkReady', { connector: oauthLink.connectorLabel })"
        :description="t('connections.oauthLinkDesc', { host: oauthLinkHost })"
      >
        <Button type="button" size="sm" @click="openOAuthLink">
          {{ t('connections.openOAuth') }}
        </Button>
        <Button type="button" size="sm" variant="outline" @click="copyOAuthLink">
          {{ t('connections.copyOAuth') }}
        </Button>
        <Button type="button" size="sm" variant="ghost" @click="oauthLink = null">
          {{ t('connections.discardOAuth') }}
        </Button>
      </SettingsRow>
    </SettingsSection>

    <SettingsSection
      v-if="editingConnection && editingMethod"
      :title="
        t('connections.replaceTitle', {
          name: editingConnection.alias || connectorLabel(editingConnection.connector_type ?? ''),
        })
      "
      data-testid="replace-credentials-section"
    >
      <CredentialFieldsForm
        :key="editingConnection.id"
        :fields="editingMethod.credential_fields ?? []"
        id-prefix="replace-credential"
        :submit-label="t('connections.replaceSubmit')"
        :busy="connectionBusyID === editingConnection.id"
        @submit="replaceCredentials"
      >
        <template #actions>
          <Button type="button" variant="outline" @click="editingConnection = null">
            {{ t('connections.cancel') }}
          </Button>
        </template>
      </CredentialFieldsForm>
    </SettingsSection>

    <div class="flex items-center justify-end px-2">
      <Input
        v-model="query"
        class="w-64"
        :placeholder="t('connections.searchPlaceholder')"
      />
    </div>

    <SettingsSection :title="t('connections.existingSection')">
      <SettingsRow
        v-for="connection in visible"
        :key="connection.id"
        :data-testid="`connection-${connection.id}`"
      >
        <template #label>
          <div class="flex items-center gap-2">
            <span class="text-control font-medium">
              {{ connection.alias || t('connections.noAlias') }}
            </span>
            <StatusBadge :status="connection.status ?? ''" />
          </div>
          <div class="mt-0.5 truncate text-body text-muted-foreground">
            {{ connection.connector_type }} · {{ connection.auth_method }} · {{ connection.id }}
          </div>
        </template>
        <Button
          v-if="isAuthorizationConnection(connection)"
          type="button"
          size="sm"
          variant="outline"
          :disabled="connectionBusyID === connection.id"
          :data-testid="`reauth-${connection.id}`"
          @click="beginReauth(connection)"
        >
          {{ t('connections.reauthorize') }}
        </Button>
        <Button
          v-if="isCredentialConnection(connection)"
          type="button"
          size="sm"
          variant="outline"
          :disabled="connectionBusyID === connection.id"
          :data-testid="`replace-${connection.id}`"
          @click="startCredentialReplacement(connection)"
        >
          {{ t('connections.replaceCredentials') }}
        </Button>
        <ArmButton
          :label="t('connections.delete')"
          :disabled="connectionBusyID === connection.id"
          @confirm="onDelete(connection)"
        />
      </SettingsRow>
      <SettingsRow
        v-if="!loading && visible.length === 0"
        :label="t('connections.empty')"
        :description="t('connections.emptyDesc')"
      />
    </SettingsSection>
  </PageShell>
</template>
