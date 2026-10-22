<script setup lang="ts">
import { computed, ref } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import type { ModelEntry } from '@/types'

const props = defineProps<{ modelValue: ModelEntry[]; catalog: string[]; disabled?: boolean; problems?: Record<string, string> }>()
const emit = defineEmits<{ 'update:modelValue': [ModelEntry[]] }>()
const filter = ref('')
const manual = ref('')
const needle = computed(() => filter.value.trim().toLowerCase())
const visible = computed(() => props.catalog.filter((name) => !needle.value || name.toLowerCase().includes(needle.value)))

function selected(name: string) {
  return props.modelValue.some((item) => item.name.toLowerCase() === name.toLowerCase())
}
function add(names: string[]) {
  const next = [...props.modelValue]
  for (const name of names) {
    const trimmed = name.trim()
    if (!trimmed || selected(trimmed)) continue
    next.push({ name: trimmed, alias: '' })
  }
  emit('update:modelValue', next)
}
function remove(name: string) {
  emit('update:modelValue', props.modelValue.filter((item) => item.name.toLowerCase() !== name.toLowerCase()))
}
function setAlias(name: string, alias: string) {
  emit('update:modelValue', props.modelValue.map((item) => item.name.toLowerCase() === name.toLowerCase() ? { ...item, alias } : item))
}
function addManual() {
  add([manual.value])
  manual.value = ''
}
</script>

<template>
  <div class="model-split">
    <div class="model-pane">
      <div class="mb-2 flex items-center justify-between gap-2">
        <strong>Catalog</strong>
        <span class="text-xs text-[var(--ink-soft)]">{{ catalog.length }} available</span>
      </div>
      <AppTextInput v-model="filter" placeholder="Filter, for example vippro/" :disabled="disabled" />
      <div class="mt-2 flex flex-wrap gap-2">
        <AppButton small :disabled="disabled || !needle || !visible.length" @click="add(visible)">Add all matching filter</AppButton>
      </div>
      <div class="model-scroll mt-2">
        <button v-for="name in visible" :key="name" type="button" class="model-row" :disabled="disabled || selected(name)" @click="add([name])">
          <span class="num truncate text-xs">{{ name }}</span>
          <span class="text-xs text-[var(--ink-soft)]">{{ selected(name) ? 'Added' : 'Add' }}</span>
        </button>
        <p v-if="!visible.length" class="py-6 text-center text-sm text-[var(--ink-soft)]">No matching models.</p>
      </div>
    </div>
    <div class="model-pane">
      <div class="mb-2 flex items-center justify-between gap-2">
        <strong>Selected</strong>
        <span class="text-xs text-[var(--ink-soft)]">{{ modelValue.length }} model{{ modelValue.length === 1 ? '' : 's' }}</span>
      </div>
      <div class="model-scroll">
        <div v-for="item in modelValue" :key="item.name" class="model-selected">
          <div class="model-selected-row">
            <span class="num model-selected-name" :title="item.name">{{ item.name }}</span>
            <AppTextInput class="model-alias" :model-value="item.alias" placeholder="Client alias" :disabled="disabled" @update:model-value="setAlias(item.name, $event)" />
            <AppButton small kind="ghost" class="model-remove" :disabled="disabled" :aria-label="`Remove ${item.name}`" @click="remove(item.name)">Remove</AppButton>
          </div>
          <p v-if="problems?.[item.name]" class="model-alias-error" role="alert">{{ problems[item.name] }}</p>
        </div>
        <p v-if="!modelValue.length" class="py-6 text-center text-sm text-[var(--ink-soft)]">No models selected.</p>
      </div>
      <form class="mt-2 flex gap-2" @submit.prevent="addManual">
        <AppTextInput v-model="manual" placeholder="Add a model name" :disabled="disabled" />
        <AppButton type="submit" :disabled="disabled || !manual.trim()">Add</AppButton>
      </form>
    </div>
  </div>
</template>
