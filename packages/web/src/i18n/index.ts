import { createI18n } from 'vue-i18n'

import en from './en'
import zh from './zh'

export type Locale = 'zh-CN' | 'en'

const storageKey = 'connect-it-locale'
const stored = localStorage.getItem(storageKey) as Locale | null

export const i18n = createI18n({
  legacy: false,
  locale: stored ?? 'zh-CN',
  fallbackLocale: 'zh-CN',
  messages: { 'zh-CN': zh, en },
})

export function setLocale(locale: Locale) {
  i18n.global.locale.value = locale
  localStorage.setItem(storageKey, locale)
  document.documentElement.lang = locale
}
