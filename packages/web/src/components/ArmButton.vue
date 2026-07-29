<script setup lang="ts">
import { Button } from '@felinic/ui'
import { onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'

// Two-step destructive action: the first click arms it and it resets after 3
// seconds; only the second click fires. A lightweight delete confirmation for
// an internal tool, without opening a dialog.
const props = defineProps<{ label: string; confirmLabel?: string; disabled?: boolean }>()
const emit = defineEmits<{ confirm: [] }>()

const { t } = useI18n()

const armed = ref(false)
let timer: ReturnType<typeof setTimeout> | null = null

function onClick() {
  if (!armed.value) {
    armed.value = true
    timer = setTimeout(() => {
      armed.value = false
    }, 3000)
    return
  }
  if (timer) clearTimeout(timer)
  armed.value = false
  emit('confirm')
}

onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
})
</script>

<template>
  <Button
    size="sm"
    :variant="armed ? 'destructive' : 'outline'"
    :disabled="props.disabled"
    @click="onClick"
  >
    {{ armed ? (props.confirmLabel ?? t('common.confirmDelete')) : props.label }}
  </Button>
</template>
