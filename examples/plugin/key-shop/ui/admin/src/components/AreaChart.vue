<script setup lang="ts">
import { computed } from 'vue'
import { formatNumber } from '@/lib/format'

const props = defineProps<{ points: Array<{ label: string; value: number }>; label: string }>()
const geometry = computed(() => {
  const width = 640
  const height = 160
  const pad = 10
  const values = props.points.map((point) => point.value)
  const max = Math.max(1, ...values, 0)
  if (!values.length) return { line: '', area: '' }
  const step = values.length === 1 ? 0 : (width - pad * 2) / (values.length - 1)
  const coords = values.map((value, index) => {
    const x = pad + step * index
    const y = pad + (1 - value / max) * (height - pad * 2)
    return [x, y] as const
  })
  const line = coords.map((point, index) => `${index ? 'L' : 'M'}${point[0].toFixed(1)},${point[1].toFixed(1)}`).join(' ')
  const last = coords[coords.length - 1]
  const first = coords[0]
  return { line, area: `${line} L${last[0].toFixed(1)},${height - pad} L${first[0].toFixed(1)},${height - pad} Z` }
})
const axis = computed(() => {
  if (props.points.length < 2) return props.points.map((point) => point.label)
  return [props.points[0], props.points[Math.floor(props.points.length / 2)], props.points[props.points.length - 1]].map((point) => point.label)
})
</script>

<template>
  <figure class="chart-frame">
    <svg v-if="points.length" viewBox="0 0 640 160" preserveAspectRatio="none" role="img" :aria-label="label">
      <path :d="geometry.area" fill="currentColor" opacity="0.14" />
      <path :d="geometry.line" fill="none" stroke="currentColor" stroke-width="2" vector-effect="non-scaling-stroke" />
    </svg>
    <p v-else class="chart-empty">No requests in this range.</p>
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
