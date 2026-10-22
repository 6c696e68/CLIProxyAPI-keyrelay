<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'
import AppButton from './AppButton.vue'

const props = defineProps<{ open: boolean; title: string; wide?: boolean; locked?: boolean }>()
const emit = defineEmits<{ close: [] }>()

function requestClose() {
  if (props.locked) return
  emit('close')
}
function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape') requestClose()
}
watch(() => props.open, (value) => {
  document.body.style.overflow = value ? 'hidden' : ''
  if (value) document.addEventListener('keydown', onKey)
  else document.removeEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.body.style.overflow = ''
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div v-if="open" class="scrim" @click.self="requestClose">
    <aside class="sheet" :class="{ wide }" role="dialog" aria-modal="true" :aria-label="title">
      <div class="mb-4 flex items-start justify-between gap-3">
        <h2 class="section-title">{{ title }}</h2>
        <AppButton class="sheet-close" small :disabled="locked" @click="requestClose">{{ locked ? 'Working...' : 'Close' }}</AppButton>
      </div>
      <slot />
    </aside>
  </div>
</template>
