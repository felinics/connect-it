<script setup lang="ts">
import type { Component } from 'vue'
import { computed } from 'vue'
import { useRoute } from 'vue-router'

// Sidebar navigation row (owner component): pass to for internal routes and
// href for external links.
// Interaction states use the overlay ladder tokens --ui-hover and
// --ui-selected.
const props = defineProps<{
  to?: string
  href?: string
  label: string
  icon: Component
}>()

const route = useRoute()
const active = computed(
  () => props.to !== undefined && (route.path === props.to || route.path.startsWith(props.to + '/')),
)

const rowClass =
  'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-control font-medium'
const idleClass = 'text-muted-foreground hover:bg-(--ui-hover) hover:text-foreground'
const activeClass = 'bg-(--ui-selected) text-foreground'
</script>

<template>
  <a
    v-if="href"
    :href="href"
    target="_blank"
    rel="noreferrer"
    :class="[rowClass, idleClass]"
  >
    <component :is="icon" class="size-4 shrink-0" />
    <span class="truncate">{{ label }}</span>
  </a>
  <RouterLink v-else :to="to ?? '/'" :class="[rowClass, active ? activeClass : idleClass]">
    <component :is="icon" class="size-4 shrink-0" />
    <span class="truncate">{{ label }}</span>
  </RouterLink>
</template>
