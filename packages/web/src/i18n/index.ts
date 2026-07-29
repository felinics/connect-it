import { createI18n } from 'vue-i18n'

import en from './en'
import zh from './zh'

export type Locale = 'zh-CN' | 'en'

const storageKey = 'connect-it-locale'
const stored = localStorage.getItem(storageKey) as Locale | null

export const i18n = createI18n({
  legacy: false,
  locale: stored ?? 'en',
  fallbackLocale: 'en',
  messages: { 'zh-CN': zh, en },
})

// index.html ships lang="en"; keep the DOM in sync when a stored locale wins.
document.documentElement.lang = i18n.global.locale.value

export function setLocale(locale: Locale) {
  i18n.global.locale.value = locale
  localStorage.setItem(storageKey, locale)
  document.documentElement.lang = locale
}
