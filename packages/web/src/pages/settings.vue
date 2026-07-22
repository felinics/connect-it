<script setup lang="ts">
import { Button, Input, Label, toast } from '@felinic/ui'
import { ref } from 'vue'

import { ApiError } from '../api/client'
import { changePassword } from '../api/endpoints'
import PageShell from '../components/PageShell.vue'
import SettingsSection from '../components/SettingsSection.vue'

const password = ref('')
const confirm = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  if (password.value.length < 8) {
    error.value = '密码至少 8 个字符'
    return
  }
  if (password.value !== confirm.value) {
    error.value = '两次输入不一致'
    return
  }
  busy.value = true
  try {
    await changePassword(password.value)
    password.value = ''
    confirm.value = ''
    toast.success('密码已修改')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '修改失败'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageShell title="设置">
    <SettingsSection title="修改密码">
      <form class="space-y-4 p-5" @submit.prevent="submit">
        <div class="space-y-1.5">
          <Label for="pw-new">新密码</Label>
          <Input id="pw-new" v-model="password" type="password" autocomplete="new-password" />
        </div>
        <div class="space-y-1.5">
          <Label for="pw-confirm">确认新密码</Label>
          <Input id="pw-confirm" v-model="confirm" type="password" autocomplete="new-password" />
        </div>
        <p v-if="error" class="text-body text-destructive">{{ error }}</p>
        <Button type="submit" :disabled="busy || password === ''">保存</Button>
      </form>
    </SettingsSection>
  </PageShell>
</template>
