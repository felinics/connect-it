<script setup lang="ts">
import { Button, Input, Label } from '@felinic/ui'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import { login } from '../api/endpoints'

const { t } = useI18n()
const router = useRouter()
const username = ref('admin')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await login(username.value, password.value)
    await router.push('/overview')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : t('login.failed')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex min-h-dvh items-center justify-center px-6">
    <form class="w-full max-w-sm space-y-4" @submit.prevent="submit">
      <div class="space-y-1">
        <h1 class="text-heading font-semibold">{{ t('app.name') }}</h1>
        <p class="text-body text-muted-foreground">{{ t('login.subtitle') }}</p>
      </div>
      <div class="space-y-1.5">
        <Label for="login-username">{{ t('login.username') }}</Label>
        <Input id="login-username" v-model="username" autocomplete="username" />
      </div>
      <div class="space-y-1.5">
        <Label for="login-password">{{ t('login.password') }}</Label>
        <Input id="login-password" v-model="password" type="password" autocomplete="current-password" />
      </div>
      <p v-if="error" class="text-body text-destructive">{{ error }}</p>
      <Button type="submit" class="w-full" :disabled="busy || password === ''">
        {{ t('login.submit') }}
      </Button>
    </form>
  </div>
</template>
