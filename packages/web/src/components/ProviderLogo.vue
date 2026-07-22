<script setup lang="ts">
import { computed, ref } from 'vue'

// Provider logo 方块：icon_url 加载失败或缺失时回退首字母。
const props = defineProps<{ name: string; iconUrl?: string }>()

const failed = ref(false)
const letter = computed(() => (props.name || '?').slice(0, 1).toUpperCase())
</script>

<template>
  <div
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
