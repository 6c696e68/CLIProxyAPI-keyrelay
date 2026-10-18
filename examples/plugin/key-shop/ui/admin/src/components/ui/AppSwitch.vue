<script setup lang="ts">
defineProps<{ on: boolean; label: string; text?: string; disabled?: boolean; compact?: boolean }>()
defineEmits<{ toggle: [] }>()
</script>

<template>
  <button type="button" class="switch" :class="{ compact }" role="switch" :aria-checked="on" :aria-label="label" :disabled="disabled" @click="$emit('toggle')">
    <span class="switch-track" aria-hidden="true"><span class="switch-thumb" /></span>
    <span v-if="text && !compact" class="switch-text">{{ text }}</span>
  </button>
</template>
