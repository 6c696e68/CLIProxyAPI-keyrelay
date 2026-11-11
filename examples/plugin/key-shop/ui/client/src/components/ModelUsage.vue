<script setup lang="ts">
import { computed, ref } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import Icon from '@/components/ui/Icon.vue'
import { prettyDateTime } from '@/lib/datetime'
import { formatNumber } from '@/lib/format'
import type { ModelUsageRow } from '@/types'

const LIMIT = 6

const props = withDefaults(defineProps<{ rows: ModelUsageRow[]; since?: string }>(), { since: '' })
const showAll = ref(false)

// Rows without generated tokens never show up, even if a server sends them.
const ranked = computed(() => props.rows.filter((row) => row.totalTokens > 0).sort((left, right) => right.totalTokens - left.totalTokens))
const totalTokens = computed(() => ranked.value.reduce((sum, row) => sum + row.totalTokens, 0))
const totalRequests = computed(() => ranked.value.reduce((sum, row) => sum + row.requests, 0))
const visible = computed(() => (showAll.value ? ranked.value : ranked.value.slice(0, LIMIT)))
const expandable = computed(() => ranked.value.length > LIMIT)
const sinceLabel = computed(() => (props.since ? `Since ${prettyDateTime(props.since)}` : ''))
const summary = computed(() => {
  const models = `${formatNumber(ranked.value.length)} ${ranked.value.length === 1 ? 'model' : 'models'}`
  return `${models} · ${compact(totalTokens.value)} tokens · ${formatNumber(totalRequests.value)} ${totalRequests.value === 1 ? 'request' : 'requests'}`
})

function compact(value: number): string {
  const number = Number(value || 0)
  if (!Number.isFinite(number) || number === 0) return '0'
  const size = Math.abs(number)
  if (size < 10_000) return number.toLocaleString('en-US')
  if (size < 1_000_000) return `${trim(number / 1_000)}K`
  if (size < 1_000_000_000) return `${trim(number / 1_000_000)}M`
  return `${trim(number / 1_000_000_000)}B`
}
function trim(value: number): string {
  if (Math.abs(value) >= 100) return String(Math.round(value))
  return value.toFixed(1).replace(/\.0$/, '')
}
function share(row: ModelUsageRow): number {
  if (!totalTokens.value) return 0
  return (row.totalTokens / totalTokens.value) * 100
}
function shareLabel(row: ModelUsageRow): string {
  const value = share(row)
  if (value > 0 && value < 1) return '<1%'
  return `${Math.round(value)}%`
}
function barStyle(row: ModelUsageRow) {
  const width = Math.max(0, Math.min(100, share(row)))
  return { width: `${width}%`, minWidth: row.totalTokens > 0 ? '2px' : '0' }
}
function mix(row: ModelUsageRow): string {
  const base = `${compact(row.inputTokens)} in · ${compact(row.outputTokens)} out`
  return row.cachedTokens > 0 ? `${base} · ${compact(row.cachedTokens)} cached` : base
}
function mixTitle(row: ModelUsageRow): string {
  const base = `${formatNumber(row.inputTokens)} input · ${formatNumber(row.outputTokens)} output`
  return row.cachedTokens > 0 ? `${base} · ${formatNumber(row.cachedTokens)} cached` : base
}
</script>

<template>
  <section v-if="ranked.length" class="panel" aria-labelledby="model-usage-title">
    <div class="panel-head">
      <div class="min-w-0">
        <h2 id="model-usage-title" class="h-section">Usage by model</h2>
        <p class="copy">{{ summary }}</p>
      </div>
      <p v-if="sinceLabel" class="eyebrow usage-since">{{ sinceLabel }}</p>
    </div>

    <table id="model-usage-table" class="usage-table usage-wide">
      <caption class="sr-only">Requests and tokens by model</caption>
      <thead>
        <tr>
          <th scope="col">Model</th>
          <th scope="col" class="col-requests is-num">Requests</th>
          <th scope="col" class="col-tokens is-num">Tokens</th>
          <th scope="col" class="col-share">Share</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in visible" :key="row.model">
          <td>
            <span class="num usage-model" :title="row.model">{{ row.model }}</span>
            <span class="usage-mix" :title="mixTitle(row)">{{ mix(row) }}</span>
          </td>
          <td class="col-requests is-num num" :title="formatNumber(row.requests)">{{ compact(row.requests) }}</td>
          <td class="col-tokens is-num num" :title="formatNumber(row.totalTokens)">{{ compact(row.totalTokens) }}</td>
          <td class="col-share">
            <span class="usage-share">
              <span class="usage-bar" aria-hidden="true"><i :style="barStyle(row)" /></span>
              <span class="num usage-pct">{{ shareLabel(row) }}</span>
            </span>
          </td>
        </tr>
      </tbody>
    </table>

    <ul id="model-usage-list" class="usage-list usage-narrow">
      <li v-for="row in visible" :key="row.model" class="usage-row">
        <span class="usage-row-top">
          <span class="num usage-model" :title="row.model">{{ row.model }}</span>
          <span class="num usage-row-tokens" :title="formatNumber(row.totalTokens)">{{ compact(row.totalTokens) }}</span>
        </span>
        <span class="usage-row-bar">
          <span class="num usage-row-requests">{{ formatNumber(row.requests) }} req</span>
          <span class="usage-bar" aria-hidden="true"><i :style="barStyle(row)" /></span>
          <span class="num usage-row-pct">{{ shareLabel(row) }}</span>
        </span>
        <span class="usage-mix" :title="mixTitle(row)">{{ mix(row) }}</span>
      </li>
    </ul>

    <div v-if="expandable" class="usage-foot">
      <AppButton
        small
        kind="ghost"
        class="usage-toggle"
        :class="{ 'is-open': showAll }"
        :aria-expanded="showAll"
        aria-controls="model-usage-table model-usage-list"
        :aria-label="showAll ? `Show fewer than ${formatNumber(ranked.length)} models` : `Show all ${formatNumber(ranked.length)} models`"
        @click="showAll = !showAll"
      >
        <span>{{ showAll ? 'Show fewer' : `Show all ${formatNumber(ranked.length)} models` }}</span>
        <Icon name="chevron" :size="14" />
      </AppButton>
    </div>
  </section>
</template>
