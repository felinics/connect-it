<script setup lang="ts">
import { computed, ref } from 'vue'

// Provider logo: falls back to the first letter when icon_url is missing or
// fails to load.
// bare is for the #icon slot of ActionCard, where a second border inside the
// card would read as a doubled outline.
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
