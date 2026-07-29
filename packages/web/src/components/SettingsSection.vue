<script setup lang="ts">
// Floating white card (owner component): title outside the card, a single
// hairline around the body, and the menu-shell radius.
defineProps<{ title?: string }>()
</script>

<template>
  <section class="space-y-2.5">
    <h2 v-if="title" class="px-2 text-label font-medium text-muted-foreground">{{ title }}</h2>
    <div class="overflow-hidden rounded-[var(--radius-menu-shell)] border border-border bg-card">
      <slot />
    </div>
  </section>
</template>
