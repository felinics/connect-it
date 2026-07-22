<script setup lang="ts">
// 页面壳（owner 组件，规范见 packages/ui/skills/web/SKILL.md「The shell」）：
// 居中列、左右留白、标题下有呼吸、区块间 space-y-8。
// wide 用于列表型页面（连接器、概览），默认宽度用于设置型页面。
defineProps<{ title: string; wide?: boolean }>()
</script>

<template>
  <div class="mx-auto px-6 pt-10 pb-12" :class="wide ? 'max-w-5xl' : 'max-w-3xl'">
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
