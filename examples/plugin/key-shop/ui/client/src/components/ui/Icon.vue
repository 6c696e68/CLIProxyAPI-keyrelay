<script setup lang="ts">
defineProps<{ name: string; size?: number }>()
</script>

<template>
  <svg :width="size ?? 16" :height="size ?? 16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <template v-if="name === 'key'">
      <circle cx="8" cy="15" r="4" />
      <path d="m11 12 9-9" />
      <path d="M16 7l3 3" />
    </template>
    <template v-else-if="name === 'menu'"><path d="M4 7h16M4 12h16M4 17h16" /></template>
    <template v-else-if="name === 'close'"><path d="m6 6 12 12M18 6 6 18" /></template>
    <template v-else-if="name === 'refresh'"><path d="M20 12a8 8 0 1 1-2.2-5.5" /><path d="M20 4v5h-5" /></template>
    <template v-else-if="name === 'sun'"><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></template>
    <template v-else-if="name === 'moon'"><path d="M16 3a8 8 0 1 0 5 13 7 7 0 0 1-5-13Z" /></template>
    <template v-else-if="name === 'logout'"><path d="M9 6H5v12h4M10 12h10M16 8l4 4-4 4" /></template>
    <template v-else-if="name === 'search'"><circle cx="11" cy="11" r="6" /><path d="m20 20-3.5-3.5" /></template>
    <template v-else-if="name === 'plus'"><path d="M12 5v14M5 12h14" /></template>
    <template v-else-if="name === 'copy'"><rect x="8" y="8" width="11" height="11" rx="2" /><path d="M5 15V5h10" /></template>
    <template v-else-if="name === 'chevron'"><path d="m8 10 4 4 4-4" /></template>
    <template v-else-if="name === 'check'"><path d="m5 12 5 5L20 7" /></template>
    <template v-else-if="name === 'panel'"><rect x="4" y="5" width="16" height="14" rx="2" /><path d="M9 5v14" /></template>
  </svg>
</template>
