<script setup lang="ts">
// Row inside a card (owner component): inset divider, fixed minimum height,
// label on the left and control on the right.
defineProps<{ label?: string; description?: string }>()
</script>

<template>
  <div class="mx-4 flex min-h-[3.75rem] items-center justify-between gap-4 border-b border-border py-3 last:border-b-0">
    <div class="min-w-0">
      <slot name="label">
        <div v-if="label" class="text-control font-medium">{{ label }}</div>
        <div v-if="description" class="mt-0.5 text-body text-muted-foreground">{{ description }}</div>
      </slot>
    </div>
    <div class="flex shrink-0 items-center gap-2">
      <slot />
    </div>
  </div>
</template>
