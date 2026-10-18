<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import Icon from './Icon.vue'

const props = defineProps<{ modelValue: string; options: Array<{ value: string; label: string }>; disabled?: boolean; label: string }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
const open = ref(false)
const root = ref<HTMLElement | null>(null)
const active = ref(0)
const current = computed(() => props.options.find((option) => option.value === props.modelValue)?.label || props.options[0]?.label || '')

function choose(index: number) {
  const option = props.options[index]
  if (!option) return
  emit('update:modelValue', option.value)
  open.value = false
}
function onKey(event: KeyboardEvent) {
  if (props.disabled) return
  if (!open.value && ['ArrowDown', 'Enter', ' '].includes(event.key)) {
    open.value = true
    event.preventDefault()
    return
  }
  if (!open.value) return
  if (event.key === 'Escape') open.value = false
  if (event.key === 'ArrowDown') active.value = Math.min(props.options.length - 1, active.value + 1)
  if (event.key === 'ArrowUp') active.value = Math.max(0, active.value - 1)
  if (event.key === 'Home') active.value = 0
  if (event.key === 'End') active.value = props.options.length - 1
  if (event.key === 'Enter') choose(active.value)
  if (event.key.length === 1) {
    const found = props.options.findIndex((option) => option.label.toLowerCase().startsWith(event.key.toLowerCase()))
    if (found >= 0) active.value = found
  }
}
function onPointer(event: PointerEvent) {
  if (!root.value?.contains(event.target as Node)) open.value = false
}
watch(open, (value) => {
  if (value) document.addEventListener('pointerdown', onPointer)
  else document.removeEventListener('pointerdown', onPointer)
})
onBeforeUnmount(() => document.removeEventListener('pointerdown', onPointer))
</script>

<template>
  <div ref="root" class="relative">
    <button type="button" class="control flex items-center justify-between gap-3 text-left" :disabled="disabled" :aria-label="label" aria-haspopup="listbox" :aria-expanded="open" @click="open = !open" @keydown="onKey">
      <span class="min-w-0 truncate">{{ current }}</span>
      <Icon name="chevron" />
    </button>
    <div v-if="open" class="menu left-0 right-0 top-[calc(100%+6px)]" role="listbox" :aria-label="label">
      <button v-for="(option, index) in options" :key="option.value || 'all'" type="button" role="option" :aria-selected="option.value === modelValue" :class="{ 'font-semibold': index === active }" @mouseenter="active = index" @click="choose(index)">
        {{ option.label }}
      </button>
    </div>
  </div>
</template>
