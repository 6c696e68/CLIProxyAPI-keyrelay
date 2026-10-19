<script setup lang="ts">
import AppSheet from '@/components/ui/AppSheet.vue'

defineProps<{ open: boolean; title: string; kicker?: string }>()
defineEmits<{ close: [] }>()
</script>

<template>
  <AppSheet :open="open" :title="title" @close="$emit('close')">
    <p v-if="kicker" class="page-kicker mb-3">{{ kicker }}</p>
    <div class="drawer-grid"><slot /></div>
    <div v-if="$slots.extra" class="mt-4"><slot name="extra" /></div>
  </AppSheet>
</template>
