<script setup lang="ts">
import { Button, Input, Label, toast } from '@felinic/ui'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { ApiError } from '../api/client'
import { changePassword } from '../api/endpoints'
import PageShell from '../components/PageShell.vue'
import SettingsSection from '../components/SettingsSection.vue'

const { t } = useI18n()
const password = ref('')
const confirm = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  if (password.value.length < 8) {
    error.value = t('settings.tooShort')
    return
  }
  if (password.value !== confirm.value) {
    error.value = t('settings.mismatch')
    return
  }
  busy.value = true
  try {
    await changePassword(password.value)
    password.value = ''
    confirm.value = ''
    toast.success(t('settings.saved'))
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : t('settings.failed')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageShell :title="t('settings.title')">
    <SettingsSection :title="t('settings.changePassword')">
      <form class="space-y-4 p-5" @submit.prevent="submit">
        <div class="space-y-1.5">
          <Label for="pw-new">{{ t('settings.newPassword') }}</Label>
          <Input id="pw-new" v-model="password" type="password" autocomplete="new-password" />
        </div>
        <div class="space-y-1.5">
          <Label for="pw-confirm">{{ t('settings.confirmPassword') }}</Label>
          <Input id="pw-confirm" v-model="confirm" type="password" autocomplete="new-password" />
        </div>
        <p v-if="error" class="text-body text-destructive">{{ error }}</p>
        <Button type="submit" :disabled="busy || password === ''">{{ t('settings.save') }}</Button>
      </form>
    </SettingsSection>
  </PageShell>
</template>
