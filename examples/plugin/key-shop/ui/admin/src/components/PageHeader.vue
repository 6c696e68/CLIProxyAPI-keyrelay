<script setup lang="ts">
defineProps<{ kicker?: string; title: string; copy?: string }>()
</script>

<template>
  <div class="page-header">
    <div class="min-w-0">
      <p v-if="kicker" class="page-kicker">{{ kicker }}</p>
      <h1 class="page-title">{{ title }}</h1>
      <p v-if="copy" class="page-copy">{{ copy }}</p>
    </div>
    <div v-if="$slots.default" class="page-actions"><slot /></div>
  </div>
</template>
