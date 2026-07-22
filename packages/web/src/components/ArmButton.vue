<script setup lang="ts">
import { Button } from '@felinic/ui'
import { onBeforeUnmount, ref } from 'vue'

// 两段式危险操作：第一次点击进入待确认态（3 秒后自动复位），
// 第二次点击才真正触发。内部工具的轻量删除确认，不开对话框。
const props = defineProps<{ label: string; confirmLabel?: string; disabled?: boolean }>()
const emit = defineEmits<{ confirm: [] }>()

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
    {{ armed ? (props.confirmLabel ?? '确认删除') : props.label }}
  </Button>
</template>
