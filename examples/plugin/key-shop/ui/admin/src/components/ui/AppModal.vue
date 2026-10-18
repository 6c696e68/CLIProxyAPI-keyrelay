<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'

const props = defineProps<{ open: boolean; title: string; kicker?: string; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()
function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape') emit('close')
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
  <div v-if="open" class="modal-scrim" @click.self="emit('close')">
    <div class="modal" :class="{ wide }" role="dialog" aria-modal="true" aria-labelledby="modal-title">
      <p v-if="kicker" class="page-kicker">{{ kicker }}</p>
      <h2 id="modal-title" class="section-title" :class="{ 'mt-1': kicker }">{{ title }}</h2>
      <div class="mt-3"><slot /></div>
    </div>
  </div>
</template>
