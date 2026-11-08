<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import Icon from './Icon.vue'

const props = defineProps<{ open: boolean; title: string; kicker?: string }>()
const emit = defineEmits<{ close: [] }>()
const dialog = ref<HTMLElement | null>(null)
let restoreFocus: HTMLElement | null = null

function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape') emit('close')
}
function lock(value: boolean) {
  document.body.style.overflow = value ? 'hidden' : ''
}
watch(() => props.open, async (value) => {
  lock(value)
  if (value) {
    restoreFocus = document.activeElement as HTMLElement | null
    document.addEventListener('keydown', onKey)
    await nextTick()
    dialog.value?.focus()
  } else {
    document.removeEventListener('keydown', onKey)
    restoreFocus?.focus?.()
    restoreFocus = null
  }
})
onBeforeUnmount(() => {
  lock(false)
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="modal-scrim" @click.self="emit('close')">
        <div ref="dialog" class="modal" role="dialog" aria-modal="true" aria-labelledby="modal-title" tabindex="-1">
          <div class="modal-handle" aria-hidden="true" />
          <div class="modal-head">
            <div class="min-w-0">
              <p v-if="kicker" class="eyebrow">{{ kicker }}</p>
              <h2 id="modal-title" class="modal-title">{{ title }}</h2>
            </div>
            <button type="button" class="btn btn-ghost btn-sm icon-btn" aria-label="Close" @click="emit('close')"><Icon name="close" /></button>
          </div>
          <div class="mt-4"><slot /></div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
