<script setup lang="ts">
import { ref } from 'vue'
import AppButton from './AppButton.vue'

defineProps<{ modelValue: string; placeholder?: string; disabled?: boolean }>()
defineEmits<{ 'update:modelValue': [string] }>()
const shown = ref(false)
</script>

<template>
  <div class="flex gap-2">
    <input
      class="control"
      :type="shown ? 'text' : 'password'"
      :value="modelValue"
      :placeholder="placeholder"
      :disabled="disabled"
      autocomplete="current-password"
      @input="$emit('update:modelValue', ($event.target as HTMLInputElement).value)"
    />
    <AppButton type="button" :aria-pressed="shown" @click="shown = !shown">{{ shown ? 'Hide' : 'Show' }}</AppButton>
  </div>
</template>
