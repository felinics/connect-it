<script setup lang="ts">
import { Button, Input, Label } from '@felinic/ui'
import { ref } from 'vue'
import { useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import { login } from '../api/endpoints'

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
    await router.push('/connectors')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '登录失败，请稍后再试'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex min-h-dvh items-center justify-center px-6">
    <form class="w-full max-w-sm space-y-4" @submit.prevent="submit">
      <div class="space-y-1">
        <h1 class="text-heading font-semibold">connect-it</h1>
        <p class="text-body text-muted-foreground">登录管理台</p>
      </div>
      <div class="space-y-1.5">
        <Label for="login-username">用户名</Label>
        <Input id="login-username" v-model="username" autocomplete="username" />
      </div>
      <div class="space-y-1.5">
        <Label for="login-password">密码</Label>
        <Input id="login-password" v-model="password" type="password" autocomplete="current-password" />
      </div>
      <p v-if="error" class="text-body text-destructive">{{ error }}</p>
      <Button type="submit" class="w-full" :disabled="busy || password === ''">登录</Button>
    </form>
  </div>
</template>
