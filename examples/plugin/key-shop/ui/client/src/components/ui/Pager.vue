<script setup lang="ts">
import AppButton from './AppButton.vue'
import Icon from './Icon.vue'

defineProps<{ note: string; page: number; hasMore: boolean; disabled?: boolean }>()
defineEmits<{ prev: []; next: [] }>()
</script>

<template>
  <nav class="mt-3 flex items-center justify-between gap-3" aria-label="Request pages">
    <span class="num text-xs text-[var(--ink-soft)]" aria-live="polite">{{ note }}</span>
    <div class="flex gap-2">
      <AppButton small :disabled="page <= 1 || disabled" aria-label="Previous page" @click="$emit('prev')"><Icon name="chevron-left" /><span class="hide-xs">Previous</span></AppButton>
      <AppButton small :disabled="!hasMore || disabled" aria-label="Next page" @click="$emit('next')"><span class="hide-xs">Next</span><Icon name="chevron-right" /></AppButton>
    </div>
  </nav>
</template>
