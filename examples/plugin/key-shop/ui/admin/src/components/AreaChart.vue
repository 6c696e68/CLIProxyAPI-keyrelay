<script setup lang="ts">
import { computed } from 'vue'
import { formatNumber } from '@/lib/format'

const props = defineProps<{ points: Array<{ label: string; value: number }>; label: string }>()
const height = 160
const width = 640
const pad = 8
const geometry = computed(() => {
  const values = props.points.map((point) => point.value)
  if (!values.length) return { line: '', area: '', max: 0, peak: 0 }
  const peak = Math.max(0, ...values)
  const max = Math.max(1, peak)
  const step = values.length === 1 ? 0 : (width - pad * 2) / (values.length - 1)
  const coords = values.map((value, index) => {
    const x = pad + step * index
    const y = pad + (1 - value / max) * (height - pad * 2)
    return [x, y] as const
  })
  const line = coords.map((point, index) => `${index ? 'L' : 'M'}${point[0].toFixed(1)},${point[1].toFixed(1)}`).join(' ')
  const last = coords[coords.length - 1]
  const first = coords[0]
  return { line, area: `${line} L${last[0].toFixed(1)},${height - pad} L${first[0].toFixed(1)},${height - pad} Z`, max, peak }
})
const ticks = computed(() => {
  const max = geometry.value.max
  // A tiny maximum would print the same rounded label twice; keep the middle
  // tick blank until the scale has room for it.
  return [max, max > 3 ? max / 2 : null, 0]
})
const axis = computed(() => {
  if (props.points.length < 2) return props.points.map((point) => point.label)
  return [props.points[0], props.points[Math.floor(props.points.length / 2)], props.points[props.points.length - 1]].map((point) => point.label)
})
</script>

<template>
  <figure class="chart-frame">
    <div class="chart-body">
      <div class="chart-y" aria-hidden="true">
        <span v-for="(tick, index) in ticks" :key="index">{{ tick === null ? '' : formatNumber(Math.round(tick)) }}</span>
      </div>
      <div class="chart-plot">
        <div class="chart-grid" aria-hidden="true"><i /><i /><i /></div>
        <svg v-if="points.length" :viewBox="`0 0 ${width} ${height}`" preserveAspectRatio="none" role="img" :aria-label="label">
          <path :d="geometry.area" fill="currentColor" opacity="0.14" />
          <path :d="geometry.line" fill="none" stroke="currentColor" stroke-width="2" vector-effect="non-scaling-stroke" />
        </svg>
        <p v-if="!geometry.peak" class="chart-empty">No requests in this range.</p>
      </div>
    </div>
    <figcaption class="chart-axis">
      <span v-for="label in axis" :key="label">{{ label }}</span>
    </figcaption>
    <table class="sr-only">
      <caption>{{ label }}</caption>
      <tbody>
        <tr v-for="point in points" :key="point.label"><th scope="row">{{ point.label }}</th><td>{{ formatNumber(point.value) }}</td></tr>
      </tbody>
    </table>
  </figure>
</template>
