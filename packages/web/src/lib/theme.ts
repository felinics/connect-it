import { ref, watchEffect } from 'vue'

export type ThemeMode = 'system' | 'light' | 'dark'

const storageKey = 'connect-it-theme'
const stored = localStorage.getItem(storageKey) as ThemeMode | null

export const themeMode = ref<ThemeMode>(stored ?? 'system')

const media = window.matchMedia('(prefers-color-scheme: dark)')

function apply() {
  const dark =
    themeMode.value === 'dark' || (themeMode.value === 'system' && media.matches)
  document.documentElement.classList.toggle('dark', dark)
}

media.addEventListener('change', apply)

watchEffect(() => {
  localStorage.setItem(storageKey, themeMode.value)
  apply()
})
