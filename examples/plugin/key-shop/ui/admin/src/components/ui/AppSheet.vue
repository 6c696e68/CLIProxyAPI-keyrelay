<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'
import AppButton from './AppButton.vue'

const props = defineProps<{ open: boolean; title: string }>()
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
  <div v-if="open" class="scrim" @click.self="emit('close')">
    <aside class="sheet" role="dialog" aria-modal="true" :aria-label="title">
      <div class="mb-4 flex items-start justify-between gap-3">
        <h2 class="section-title">{{ title }}</h2>
        <AppButton small @click="emit('close')">Close</AppButton>
      </div>
      <slot />
    </aside>
  </div>
</template>
