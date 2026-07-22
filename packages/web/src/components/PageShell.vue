<script setup lang="ts">
// 页面壳（owner 组件，规范见 packages/ui/skills/web/SKILL.md「The shell」）：
// 居中 max-w-3xl、左右留白、标题下有呼吸、区块间 space-y-8。
defineProps<{ title: string }>()
</script>

<template>
  <div class="mx-auto max-w-3xl px-6 pt-10 pb-12">
    <div class="mb-6 flex items-center justify-between px-2">
      <h1 class="text-heading font-semibold">{{ title }}</h1>
      <div class="flex items-center gap-2">
        <slot name="actions" />
      </div>
    </div>
    <div class="space-y-8">
      <slot />
    </div>
  </div>
</template>
