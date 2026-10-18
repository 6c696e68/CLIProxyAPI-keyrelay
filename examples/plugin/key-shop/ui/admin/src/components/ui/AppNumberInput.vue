<script setup lang="ts">
defineProps<{ modelValue: number; min?: number; max?: number; disabled?: boolean }>()
defineEmits<{ 'update:modelValue': [number] }>()
</script>

<template>
  <input
    class="control num"
    type="text"
    inputmode="numeric"
    :value="modelValue"
    :disabled="disabled"
    @input="$emit('update:modelValue', Number(($event.target as HTMLInputElement).value.replace(/[^\d]/g, '')) || 0)"
  />
</template>
