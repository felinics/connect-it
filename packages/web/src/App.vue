<script setup lang="ts">
import { Button, NativeSelect, NativeSelectOption, Toaster } from '@felinic/ui'
import {
  CubeIcon,
  DesktopIcon,
  GearIcon,
  HomeIcon,
  Link2Icon,
  LockClosedIcon,
  MoonIcon,
  ReaderIcon,
  SunIcon,
} from '@radix-icons/vue'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'

import { healthz } from './api/endpoints'
import SidebarNavItem from './components/SidebarNavItem.vue'
import { setLocale, type Locale } from './i18n'
import { themeMode, type ThemeMode } from './lib/theme'

const route = useRoute()
const { t, locale } = useI18n()

const nav = computed(() => [
  { to: '/overview', label: t('nav.overview'), icon: HomeIcon },
  { to: '/connectors', label: t('nav.connectors'), icon: CubeIcon },
  { to: '/connections', label: t('nav.connections'), icon: Link2Icon },
  { to: '/tokens', label: t('nav.tokens'), icon: LockClosedIcon },
  { to: '/settings', label: t('nav.settings'), icon: GearIcon },
])

const themeOptions = computed(() => [
  { value: 'system' as ThemeMode, icon: DesktopIcon, label: t('sidebar.themeSystem') },
  { value: 'light' as ThemeMode, icon: SunIcon, label: t('sidebar.themeLight') },
  { value: 'dark' as ThemeMode, icon: MoonIcon, label: t('sidebar.themeDark') },
])

const currentLocale = computed({
  get: () => locale.value as Locale,
  set: (v: Locale) => setLocale(v),
})

// Service health probe driving the status dot at the bottom of the sidebar.
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
        <div class="px-4 pt-5 pb-3 text-title font-semibold">{{ t('app.name') }}</div>

        <nav class="flex-1 space-y-0.5 overflow-y-auto px-2.5">
          <SidebarNavItem
            v-for="item in nav"
            :key="item.to"
            :to="item.to"
            :label="item.label"
            :icon="item.icon"
          />
          <SidebarNavItem href="/swagger/index.html" :label="t('nav.docs')" :icon="ReaderIcon" />
        </nav>

        <div class="space-y-3 border-t border-border px-4 py-4">
          <div class="flex items-center justify-between gap-2">
            <span class="text-caption text-muted-foreground">{{ t('sidebar.language') }}</span>
            <NativeSelect v-model="currentLocale" size="sm" class="w-28">
              <NativeSelectOption value="zh-CN">简体中文</NativeSelectOption>
              <NativeSelectOption value="en">English</NativeSelectOption>
            </NativeSelect>
          </div>
          <div class="flex items-center justify-between gap-2">
            <span class="text-caption text-muted-foreground">{{ t('sidebar.theme') }}</span>
            <div class="flex items-center gap-0.5">
              <Button
                v-for="opt in themeOptions"
                :key="opt.value"
                variant="ghost"
                size="icon-sm"
                :aria-label="opt.label"
                :data-ui-selected="themeMode === opt.value ? '' : undefined"
                @click="themeMode = opt.value"
              >
                <component :is="opt.icon" />
              </Button>
            </div>
          </div>
          <div class="flex items-center gap-1.5 text-caption text-muted-foreground">
            <span
              class="size-1.5 rounded-full"
              :style="{ backgroundColor: healthy ? 'var(--accent-green)' : 'var(--destructive)' }"
            />
            <span>{{ healthy ? t('sidebar.healthy') : t('sidebar.unreachable') }}</span>
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
