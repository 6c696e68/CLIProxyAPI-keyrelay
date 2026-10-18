<script setup lang="ts">
defineProps<{ modelValue: string; type?: string; placeholder?: string; disabled?: boolean; maxlength?: number }>()
defineEmits<{ 'update:modelValue': [string] }>()
</script>

<template>
  <input
    class="control"
    :type="type || 'text'"
    :value="modelValue"
    :placeholder="placeholder"
    :disabled="disabled"
    :maxlength="maxlength"
    autocomplete="off"
    spellcheck="false"
    @input="$emit('update:modelValue', ($event.target as HTMLInputElement).value)"
  />
</template>
