<script setup lang="ts">
import { SegmentedControl, Toaster } from '@felinic/ui'
import { House, KeyRound, Link2, Plug, Settings, SquareLibrary } from 'lucide-vue-next'
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'

import { healthz } from './api/endpoints'
import SidebarNavItem from './components/SidebarNavItem.vue'
import { themeMode } from './lib/theme'

const route = useRoute()

const nav = [
  { to: '/overview', label: '概览', icon: House },
  { to: '/connectors', label: '连接器', icon: Plug },
  { to: '/connections', label: '连接', icon: Link2 },
  { to: '/tokens', label: 'API Token', icon: KeyRound },
  { to: '/settings', label: '设置', icon: Settings },
]

const themeItems = [
  { value: 'system' as const, label: '自动' },
  { value: 'light' as const, label: '亮' },
  { value: 'dark' as const, label: '暗' },
]

// 服务健康探测：驱动侧栏底部的状态点。
const healthy = ref(true)
let timer: ReturnType<typeof setInterval> | null = null

async function ping() {
  try {
    await healthz()
    healthy.value = true
  } catch {
    healthy.value = false
  }
}

onMounted(() => {
  void ping()
  timer = setInterval(() => void ping(), 30_000)
})
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div class="flex min-h-dvh bg-background text-foreground">
    <template v-if="route.path !== '/login'">
      <aside class="sticky top-0 flex h-dvh w-60 shrink-0 flex-col border-r border-border">
        <div class="flex items-center gap-2.5 px-4 pt-5 pb-4">
          <div class="flex size-9 items-center justify-center rounded-md border border-border">
            <Plug class="size-4" />
          </div>
          <div class="min-w-0">
            <div class="text-control font-semibold">connect-it</div>
            <div class="text-caption text-muted-foreground">内部连接器控制台</div>
          </div>
        </div>

        <nav class="flex-1 space-y-0.5 overflow-y-auto px-2.5">
          <SidebarNavItem
            v-for="item in nav"
            :key="item.to"
            :to="item.to"
            :label="item.label"
            :icon="item.icon"
          />
          <SidebarNavItem href="/swagger/index.html" label="API 文档" :icon="SquareLibrary" />
        </nav>

        <div class="space-y-3 border-t border-border px-4 py-4">
          <div class="flex items-center justify-between gap-2">
            <span class="text-caption text-muted-foreground">主题</span>
            <SegmentedControl v-model="themeMode" :items="themeItems" aria-label="主题" />
          </div>
          <div class="flex items-center gap-1.5 text-caption text-muted-foreground">
            <span
              class="size-1.5 rounded-full"
              :style="{ backgroundColor: healthy ? 'var(--accent-green)' : 'var(--destructive)' }"
            />
            <span>{{ healthy ? '服务正常' : '服务不可达' }}</span>
          </div>
        </div>
      </aside>
      <main class="min-w-0 flex-1">
        <RouterView />
      </main>
    </template>
    <template v-else>
      <div class="flex-1">
        <RouterView />
      </div>
    </template>
    <Toaster />
  </div>
</template>
