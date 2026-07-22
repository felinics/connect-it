<script setup lang="ts">
import { Button, Input, Label, TextButton, toast } from '@felinic/ui'
import { Copy } from 'lucide-vue-next'
import { onMounted, ref } from 'vue'

import { ApiError } from '../api/client'
import { createApiToken, deleteApiToken, listApiTokens } from '../api/endpoints'
import type { ApiToken } from '../api/types'
import ArmButton from '../components/ArmButton.vue'
import PageShell from '../components/PageShell.vue'
import SettingsRow from '../components/SettingsRow.vue'
import SettingsSection from '../components/SettingsSection.vue'

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
    toast.error(e instanceof ApiError ? e.message : '加载 token 失败')
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
    toast.error(e instanceof ApiError ? e.message : '创建失败')
  } finally {
    busy.value = false
  }
}

async function copyToken() {
  await navigator.clipboard.writeText(createdToken.value)
  toast.success('已复制到剪贴板')
}

async function onRevoke(token: ApiToken) {
  try {
    await deleteApiToken(token.id ?? '')
    toast.success(`Token「${token.name}」已撤销`)
    await reload()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '撤销失败')
  }
}
</script>

<template>
  <PageShell title="API Token">
    <SettingsSection title="新建 token" >
      <form class="space-y-4 p-5" @submit.prevent="onCreate">
        <div class="space-y-1.5">
          <Label for="token-name">名称</Label>
          <Input id="token-name" v-model="name" placeholder="如 chatbot-prod" />
          <p class="text-body text-muted-foreground">内部应用用它调用 /v1 接口与签发 MCP session。</p>
        </div>
        <Button type="submit" :disabled="busy || name.trim() === ''">创建</Button>
        <div v-if="createdToken" class="space-y-1.5">
          <p class="text-body text-destructive">明文只显示这一次，请立即保存：</p>
          <div class="flex items-center gap-2">
            <code class="min-w-0 flex-1 truncate text-body">{{ createdToken }}</code>
            <TextButton type="button" @click="copyToken"><Copy />复制</TextButton>
          </div>
        </div>
      </form>
    </SettingsSection>

    <SettingsSection title="已有 token">
      <SettingsRow
        v-for="token in tokens"
        :key="token.id"
        :label="token.name"
        :description="token.revoked_at ? `已于 ${token.revoked_at} 撤销` : `创建于 ${token.created_at}`"
      >
        <ArmButton v-if="!token.revoked_at" label="撤销" confirm-label="确认撤销" @confirm="onRevoke(token)" />
      </SettingsRow>
      <SettingsRow v-if="!loading && tokens.length === 0" label="还没有任何 token" />
    </SettingsSection>
  </PageShell>
</template>
