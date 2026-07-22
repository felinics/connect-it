<script setup lang="ts">
import { Button } from '@felinic/ui'
import { ArrowLeftIcon } from '@radix-icons/vue'
import { useRouter } from 'vue-router'

// 页面壳（owner 组件，规范见 packages/ui/skills/web/SKILL.md「The shell」）：
// 居中列、左右留白、标题下有呼吸、区块间 space-y-8。
// wide 用于列表型页面（连接器、概览）；backTo 渲染标题左侧的返回按钮。
const props = defineProps<{ title: string; wide?: boolean; backTo?: string }>()

const router = useRouter()

function goBack() {
  if (props.backTo) void router.push(props.backTo)
}
</script>

<template>
  <div class="mx-auto px-6 pt-10 pb-12" :class="wide ? 'max-w-5xl' : 'max-w-3xl'">
    <div class="mb-6 flex items-center justify-between px-2">
      <div class="flex min-w-0 items-center gap-2">
        <Button
          v-if="backTo"
          variant="ghost"
          size="icon-sm"
          aria-label="back"
          @click="goBack"
        >
          <ArrowLeftIcon />
        </Button>
        <h1 class="truncate text-heading font-semibold">{{ title }}</h1>
      </div>
      <div class="flex items-center gap-2">
        <slot name="actions" />
      </div>
    </div>
    <div class="space-y-8">
      <slot />
    </div>
  </div>
</template>
