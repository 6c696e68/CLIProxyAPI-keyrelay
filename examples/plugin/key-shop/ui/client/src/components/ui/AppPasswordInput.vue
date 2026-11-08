<script setup lang="ts">
import { ref } from 'vue'
import Icon from './Icon.vue'

defineProps<{ modelValue: string; placeholder?: string; disabled?: boolean; invalid?: boolean }>()
defineEmits<{ 'update:modelValue': [string] }>()
const shown = ref(false)
</script>

<template>
  <div class="input-wrap">
    <input
      class="control num"
      :type="shown ? 'text' : 'password'"
      :value="modelValue"
      :placeholder="placeholder"
      :disabled="disabled"
      :aria-invalid="invalid || undefined"
      autocomplete="current-password"
      autocapitalize="off"
      spellcheck="false"
      @input="$emit('update:modelValue', ($event.target as HTMLInputElement).value)"
    />
    <button
      type="button"
      class="btn btn-ghost btn-sm icon-btn input-action"
      :aria-pressed="shown"
      :aria-label="shown ? 'Hide key' : 'Show key'"
      :disabled="disabled"
      @click="shown = !shown"
    >
      <Icon :name="shown ? 'eye-off' : 'eye'" />
    </button>
  </div>
</template>
