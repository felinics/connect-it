<script setup lang="ts">
import { Toaster } from '@felinic/ui'
import { useRoute } from 'vue-router'

const route = useRoute()

const nav = [
  { to: '/connectors', label: '连接器' },
  { to: '/connections', label: '连接' },
  { to: '/tokens', label: 'API Token' },
  { to: '/settings', label: '设置' },
]
</script>

<template>
  <div class="min-h-dvh bg-background text-foreground">
    <!-- 顶部导航：ui 库无 Link 组件，这里是全应用唯一的手写 token 样式链接。 -->
    <header v-if="route.path !== '/login'" class="border-b border-border">
      <nav class="mx-auto flex max-w-3xl items-center gap-1 px-6 py-3">
        <span class="mr-4 text-control font-semibold">connect-it</span>
        <RouterLink
          v-for="item in nav"
          :key="item.to"
          :to="item.to"
          class="rounded-sm px-1.5 py-1 text-control font-medium"
          :class="route.path.startsWith(item.to) ? 'text-foreground' : 'text-muted-foreground hover:text-foreground'"
        >
          {{ item.label }}
        </RouterLink>
      </nav>
    </header>
    <RouterView />
    <Toaster />
  </div>
</template>
