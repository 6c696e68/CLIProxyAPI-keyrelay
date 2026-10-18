<script setup lang="ts">
import AppSelect from './ui/AppSelect.vue'
import type { Scenario } from '@/types'

defineProps<{ scenario: Scenario }>()
defineEmits<{ 'update:scenario': [Scenario] }>()
const options = [
  { value: 'ready', label: 'Ready' },
  { value: 'loading', label: 'Loading' },
  { value: 'empty', label: 'Empty' },
  { value: 'error', label: 'Error' },
  { value: 'limited', label: 'Limited' },
]
</script>

<template>
  <div class="preview">
    <span>Preview</span>
    <div class="w-32"><AppSelect :model-value="scenario" :options="options" label="Preview scenario" placement="up" @update:model-value="$emit('update:scenario', $event as Scenario)" /></div>
  </div>
</template>
