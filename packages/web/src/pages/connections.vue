<script setup lang="ts">
import { Button, Input, toast } from '@felinic/ui'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { ApiError } from '../api/client'
import { adminDeleteConnection, adminReauthConnection, listConnections } from '../api/endpoints'
import type { Connection } from '../api/types'
import ArmButton from '../components/ArmButton.vue'
import PageShell from '../components/PageShell.vue'
import SettingsRow from '../components/SettingsRow.vue'
import SettingsSection from '../components/SettingsSection.vue'
import StatusBadge from '../components/StatusBadge.vue'

const { t } = useI18n()

const connections = ref<Connection[]>([])
const loading = ref(true)
const query = ref('')

async function reload() {
  connections.value = await listConnections()
}

onMounted(async () => {
  try {
    await reload()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('connections.loadFailed'))
  } finally {
    loading.value = false
  }
})

const visible = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (q === '') return connections.value
  return connections.value.filter((c) =>
    [c.alias, c.connector_type, c.id, c.auth_method]
      .filter((s): s is string => typeof s === 'string')
      .some((s) => s.toLowerCase().includes(q)),
  )
})

// Mint and copy a re-authorization link for an operator to hand to the right
// end user.
async function copyReauthLink(conn: Connection) {
  try {
    const result = await adminReauthConnection(conn.id ?? '')
    if (result.authorization_url) {
      await navigator.clipboard.writeText(result.authorization_url)
      toast.success(t('connections.reauthCopied'))
    }
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('connections.reauthFailed'))
  }
}

async function onDelete(conn: Connection) {
  try {
    await adminDeleteConnection(conn.id ?? '')
    toast.success(t('connections.deleted'))
    await reload()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('connections.deleteFailed'))
  }
}
</script>

<template>
  <PageShell :title="t('connections.title')" wide>
    <p class="px-2 text-body text-muted-foreground">{{ t('connections.subtitle') }}</p>

    <div class="flex items-center justify-end px-2">
      <Input v-model="query" class="w-64" :placeholder="t('connections.searchPlaceholder')" />
    </div>

    <SettingsSection>
      <SettingsRow v-for="conn in visible" :key="conn.id">
        <template #label>
          <div class="flex items-center gap-2">
            <span class="text-control font-medium">
              {{ conn.alias || t('connections.noAlias') }}
            </span>
            <StatusBadge :status="conn.status ?? ''" />
          </div>
          <div class="mt-0.5 truncate text-body text-muted-foreground">
            {{ conn.connector_type }} · {{ conn.auth_method }} · {{ conn.id }}
          </div>
        </template>
        <Button size="sm" variant="outline" @click="copyReauthLink(conn)">
          {{ t('connections.copyReauth') }}
        </Button>
        <ArmButton :label="t('connections.delete')" @confirm="onDelete(conn)" />
      </SettingsRow>
      <SettingsRow
        v-if="!loading && visible.length === 0"
        :label="t('connections.empty')"
        :description="t('connections.emptyDesc')"
      />
    </SettingsSection>
  </PageShell>
</template>
