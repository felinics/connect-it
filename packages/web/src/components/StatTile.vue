<script setup lang="ts">
// 概览页统计块（section-level stat tile）：标签在上、数值在下的独立白卡。
defineProps<{ label: string; value: string | number; hint?: string }>()
</script>

<template>
  <div class="rounded-[var(--radius-menu-shell)] border border-border bg-card p-4">
    <div class="text-label text-muted-foreground">{{ label }}</div>
    <div class="mt-1 text-display font-semibold">{{ value }}</div>
    <div v-if="hint" class="mt-0.5 text-body text-muted-foreground">{{ hint }}</div>
  </div>
</template>
