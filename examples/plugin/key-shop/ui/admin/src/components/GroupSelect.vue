<script setup lang="ts">
import { computed } from 'vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import type { ModelGroup } from '@/types'

const props = defineProps<{
  modelValue: string
  groups: ModelGroup[]
  label: string
  includeAll?: boolean
  includeNone?: boolean
  disabled?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
const options = computed(() => {
  const items = props.groups.map((group) => ({ value: group.id, label: group.isSystem ? `${group.name} · built-in` : group.name }))
  if (props.includeNone) items.unshift({ value: 'none', label: 'No group' })
  if (props.includeAll) items.unshift({ value: '', label: 'All groups' })
  return items
})
</script>

<template>
  <AppSelect :model-value="modelValue" :options="options" :label="label" :disabled="disabled || !options.length" @update:model-value="emit('update:modelValue', $event)" />
</template>
