<script setup lang="ts">
import { Button, Input, Label, TextButton, toast } from '@felinic/ui'
import { CopyIcon } from '@radix-icons/vue'
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { ApiError } from '../api/client'
import { createApiToken, deleteApiToken, listApiTokens } from '../api/endpoints'
import type { ApiToken } from '../api/types'
import ArmButton from '../components/ArmButton.vue'
import PageShell from '../components/PageShell.vue'
import SettingsRow from '../components/SettingsRow.vue'
import SettingsSection from '../components/SettingsSection.vue'

const { t } = useI18n()

const tokens = ref<ApiToken[]>([])
const loading = ref(true)
const busy = ref(false)
const name = ref('')
const createdToken = ref('')

async function reload() {
  tokens.value = await listApiTokens()
}

onMounted(async () => {
  try {
    await reload()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('tokens.loadFailed'))
  } finally {
    loading.value = false
  }
})

async function onCreate() {
  busy.value = true
  try {
    const res = await createApiToken(name.value)
    createdToken.value = res.token ?? ''
    name.value = ''
    await reload()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('tokens.createFailed'))
  } finally {
    busy.value = false
  }
}

async function copyToken() {
  await navigator.clipboard.writeText(createdToken.value)
  toast.success(t('common.copied'))
}

async function onRevoke(token: ApiToken) {
  try {
    await deleteApiToken(token.id ?? '')
    toast.success(t('tokens.revoked', { name: token.name }))
    await reload()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : t('tokens.revokeFailed'))
  }
}
</script>

<template>
  <PageShell :title="t('tokens.title')">
    <SettingsSection :title="t('tokens.create')">
      <form class="space-y-4 p-5" @submit.prevent="onCreate">
        <div class="space-y-1.5">
          <Label for="token-name">{{ t('tokens.name') }}</Label>
          <Input id="token-name" v-model="name" :placeholder="t('tokens.namePlaceholder')" />
          <p class="text-body text-muted-foreground">{{ t('tokens.nameHint') }}</p>
        </div>
        <Button type="submit" :disabled="busy || name.trim() === ''">{{ t('tokens.submit') }}</Button>
        <div v-if="createdToken" class="space-y-1.5">
          <p class="text-body text-destructive">{{ t('tokens.plaintextOnce') }}</p>
          <div class="flex items-center gap-2">
            <code class="min-w-0 flex-1 truncate text-body">{{ createdToken }}</code>
            <TextButton type="button" @click="copyToken"><CopyIcon />{{ t('common.copy') }}</TextButton>
          </div>
        </div>
      </form>
    </SettingsSection>

    <SettingsSection :title="t('tokens.existing')">
      <SettingsRow
        v-for="token in tokens"
        :key="token.id"
        :label="token.name"
        :description="token.revoked_at ? t('tokens.revokedAt', { time: token.revoked_at }) : t('tokens.createdAt', { time: token.created_at })"
      >
        <ArmButton v-if="!token.revoked_at" :label="t('tokens.revoke')" :confirm-label="t('tokens.confirmRevoke')" @confirm="onRevoke(token)" />
      </SettingsRow>
      <SettingsRow v-if="!loading && tokens.length === 0" :label="t('tokens.empty')" />
    </SettingsSection>
  </PageShell>
</template>
