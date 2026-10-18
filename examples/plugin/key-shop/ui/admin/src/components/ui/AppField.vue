<script setup lang="ts">
defineProps<{ label: string; hint?: string; error?: string }>()
</script>

<template>
  <label class="field">
    <span class="field-label">{{ label }}</span>
    <slot />
    <span v-if="error" class="field-error" role="alert">{{ error }}</span>
    <span v-else-if="hint" class="field-hint">{{ hint }}</span>
  </label>
</template>
