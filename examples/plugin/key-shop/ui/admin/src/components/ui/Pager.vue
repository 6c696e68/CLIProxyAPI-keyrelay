<script setup lang="ts">
import AppButton from './AppButton.vue'

defineProps<{ note: string; page: number; hasMore: boolean; disabled?: boolean }>()
defineEmits<{ prev: []; next: [] }>()
</script>

<template>
  <div class="mt-3 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
    <span class="text-xs text-[var(--ink-soft)]">{{ note }}</span>
    <div class="grid grid-cols-2 gap-2 sm:flex">
      <AppButton small :disabled="page <= 1 || disabled" @click="$emit('prev')">Previous</AppButton>
      <AppButton small :disabled="!hasMore || disabled" @click="$emit('next')">Next</AppButton>
    </div>
  </div>
</template>
