<script setup lang="ts">
import { Button } from '@felinic/ui'
import { ArrowLeftIcon } from '@radix-icons/vue'
import { useRouter } from 'vue-router'

// Page shell (owner component; see "The shell" in
// packages/ui/skills/web/SKILL.md): a centred column with side gutters,
// breathing room under the title, and space-y-8 between sections.
// wide suits list pages such as connectors and overview; backTo renders a
// back button to the left of the title.
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
