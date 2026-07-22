<script setup lang="ts">
import { computed, ref } from 'vue'

// Provider logo：icon_url 加载失败或缺失时回退首字母。
// bare 用于 ActionCard 的 #icon 插槽（卡内不再套第二层描边，避免双描边脏样式）。
const props = defineProps<{ name: string; iconUrl?: string; bare?: boolean }>()

const failed = ref(false)
const letter = computed(() => (props.name || '?').slice(0, 1).toUpperCase())
</script>

<template>
  <span v-if="bare" class="flex size-6 shrink-0 items-center justify-center">
    <img
      v-if="iconUrl && !failed"
      :src="iconUrl"
      :alt="name"
      class="size-5"
      @error="failed = true"
    />
    <span v-else class="text-label font-semibold text-muted-foreground">{{ letter }}</span>
  </span>
  <div
    v-else
    class="flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-md border border-border bg-card"
  >
    <img
      v-if="iconUrl && !failed"
      :src="iconUrl"
      :alt="name"
      class="size-6"
      @error="failed = true"
    />
    <span v-else class="text-label font-semibold text-muted-foreground">{{ letter }}</span>
  </div>
</template>
