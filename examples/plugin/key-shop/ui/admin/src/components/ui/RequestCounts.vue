<script setup lang="ts">
import { ref } from 'vue'
import { formatNumber } from '@/lib/format'

const props = defineProps<{
  succeeded: number
  failed: number
  canceled: number
  rejected: number
}>()

const tip = ref<{ text: string; x: number; y: number } | null>(null)
const items = [
  { key: 'succeeded', tone: 'good', label: 'succeeded' },
  { key: 'failed', tone: 'bad', label: 'failed' },
  { key: 'canceled', tone: 'warn', label: 'canceled' },
  { key: 'rejected', tone: 'muted', label: 'rejected' },
] as const

function valueOf(key: (typeof items)[number]['key']) {
  return props[key]
}
function show(event: MouseEvent | FocusEvent, label: string, value: number) {
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  tip.value = { text: `${formatNumber(value)} ${label}`, x: rect.left + rect.width / 2, y: rect.top }
}
function hide() {
  tip.value = null
}
</script>

<template>
  <span class="count-row">
    <template v-for="(item, index) in items" :key="item.key">
      <span v-if="index" class="count-dot" aria-hidden="true">·</span>
      <span
        class="count-num"
        :class="`tone-${item.tone}`"
        tabindex="0"
        :aria-label="`${formatNumber(valueOf(item.key))} ${item.label}`"
        @mouseenter="show($event, item.label, valueOf(item.key))"
        @mouseleave="hide"
        @focus="show($event, item.label, valueOf(item.key))"
        @blur="hide"
      >{{ formatNumber(valueOf(item.key)) }}</span>
    </template>
    <Teleport to="body">
      <span v-if="tip" class="count-tip" role="tooltip" :style="{ left: `${tip.x}px`, top: `${tip.y}px` }">{{ tip.text }}</span>
    </Teleport>
  </span>
</template>

<style scoped>
.count-row { display: inline-flex; align-items: baseline; gap: 0.35rem; font-variant-numeric: tabular-nums; }
.count-dot { color: var(--ink-faint); }
.count-num { font-weight: 680; cursor: default; outline: none; }
.count-num:focus-visible { box-shadow: var(--focus); border-radius: 4px; }
.tone-good { color: var(--good); }
.tone-bad { color: var(--bad); }
.tone-warn { color: var(--warn); }
.tone-muted { color: var(--ink-soft); }
.count-tip {
  position: fixed;
  z-index: 90;
  transform: translate(-50%, calc(-100% - 8px));
  border-radius: 8px;
  background: var(--ink);
  color: var(--bg-page);
  padding: 4px 8px;
  font-size: 12px;
  font-weight: 650;
  white-space: nowrap;
  pointer-events: none;
}
</style>
