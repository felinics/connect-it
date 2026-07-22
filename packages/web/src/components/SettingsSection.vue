<script setup lang="ts">
// 白色浮卡（owner 组件）：标题在卡外、卡体一条 hairline、menu-shell 半径。
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
