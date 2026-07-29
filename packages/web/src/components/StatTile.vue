<script setup lang="ts">
// Section-level stat tile for the overview page: a standalone white card with
// the label above and the value below.
defineProps<{ label: string; value: string | number; hint?: string }>()
</script>

<template>
  <div class="rounded-[var(--radius-menu-shell)] border border-border bg-card p-4">
    <div class="text-label text-muted-foreground">{{ label }}</div>
    <div class="mt-1 text-display font-semibold">{{ value }}</div>
    <div v-if="hint" class="mt-0.5 text-body text-muted-foreground">{{ hint }}</div>
  </div>
</template>
