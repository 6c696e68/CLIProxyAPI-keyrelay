<script setup lang="ts">
defineProps<{ label: string; value: string | number; note?: string; quiet?: boolean }>()
</script>

<template>
  <article v-if="!quiet" class="panel metric">
    <span class="metric-label">{{ label }}</span>
    <strong class="metric-value num">{{ value }}</strong>
    <span v-if="note" class="metric-note">{{ note }}</span>
  </article>
  <div v-else class="quiet-metric">
    <span class="metric-label">{{ label }}</span>
    <strong class="num mt-1 block text-base">{{ value }}</strong>
    <span v-if="note" class="metric-note">{{ note }}</span>
  </div>
</template>
