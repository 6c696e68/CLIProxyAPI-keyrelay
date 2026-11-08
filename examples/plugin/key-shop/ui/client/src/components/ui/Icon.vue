<script setup lang="ts">
defineProps<{ name: string; size?: number }>()
</script>

<template>
  <svg :width="size ?? 16" :height="size ?? 16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
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
    <template v-else-if="name === 'chevron-right'"><path d="m10 8 4 4-4 4" /></template>
    <template v-else-if="name === 'chevron-left'"><path d="m14 8-4 4 4 4" /></template>
    <template v-else-if="name === 'check'"><path d="m5 12 5 5L20 7" /></template>
    <template v-else-if="name === 'panel'"><rect x="4" y="5" width="16" height="14" rx="2" /><path d="M9 5v14" /></template>
    <template v-else-if="name === 'calendar'"><rect x="4" y="5" width="16" height="15" rx="2" /><path d="M8 3v4M16 3v4M4 10h16" /></template>
    <template v-else-if="name === 'clock'"><circle cx="12" cy="12" r="8" /><path d="M12 8v4l3 2" /></template>
    <template v-else-if="name === 'hourglass'"><path d="M7 3h10M7 21h10M8 3c0 5 8 5 8 9s-8 4-8 9M16 3c0 5-8 5-8 9" /></template>
    <template v-else-if="name === 'activity'"><path d="M3 12h4l3-7 4 14 3-7h4" /></template>
    <template v-else-if="name === 'alert'"><circle cx="12" cy="12" r="8" /><path d="M12 8v5M12 16h.01" /></template>
    <template v-else-if="name === 'eye'"><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12Z" /><circle cx="12" cy="12" r="2.5" /></template>
    <template v-else-if="name === 'eye-off'"><path d="M4 4l16 16" /><path d="M10.6 6.1A9.6 9.6 0 0 1 12 6c6 0 9.5 6 9.5 6a16 16 0 0 1-2.6 3.3M6.6 7.6C4 9.3 2.5 12 2.5 12S6 18 12 18c1.5 0 2.9-.4 4-1" /><path d="M9.9 10a2.5 2.5 0 0 0 3.6 3.5" /></template>
    <template v-else-if="name === 'layers'"><path d="m12 4 8 4-8 4-8-4 8-4Z" /><path d="m4 12 8 4 8-4M4 16l8 4 8-4" /></template>
    <template v-else-if="name === 'inbox'"><path d="M4 13h4l1.5 3h5L16 13h4" /><path d="M5.5 6h13L20 13v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1v-5l1.5-7Z" /></template>
    <template v-else-if="name === 'shield'"><path d="M12 3 5 6v5c0 4.5 3 8.3 7 10 4-1.7 7-5.5 7-10V6l-7-3Z" /><path d="m9 12 2 2 4-4" /></template>
    <template v-else-if="name === 'link'"><path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1" /><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1" /></template>
  </svg>
</template>
