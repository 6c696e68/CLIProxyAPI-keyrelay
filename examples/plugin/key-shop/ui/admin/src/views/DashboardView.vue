<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import AreaChart from '@/components/AreaChart.vue'
import MetricCard from '@/components/MetricCard.vue'
import PageHeader from '@/components/PageHeader.vue'
import RankList from '@/components/RankList.vue'
import StatusChip from '@/components/ui/StatusChip.vue'
import { useLoadGuard } from '@/composables/useLoadGuard'
import { bindViewBusy, notifyKey, refreshKey } from '@/composables/useToast'
import { formatMs, formatNumber, formatPercent, splitWhen } from '@/lib/format'
import { presetRange, type RangePreset } from '@/lib/range'
import { fillSeries } from '@/lib/series'
import type { AdminService, RequestRow, Scenario, Stats, UsageReport } from '@/types'

const props = withDefaults(defineProps<{ service: AdminService; scenario?: Scenario }>(), { scenario: 'ready' })
const notify = inject(notifyKey, () => {})
const refreshTick = inject(refreshKey, ref(0))
const stats = ref<Stats | null>(null)
const usage = ref<UsageReport | null>(null)
const latest = ref<RequestRow[]>([])
const error = ref('')
const { loading, load: runLoad } = useLoadGuard()
bindViewBusy(loading)
const preset = ref<RangePreset>('24h')
const presets: RangePreset[] = ['1h', '24h', '7d', '30d']

const chartPoints = computed(() => {
  if (!usage.value) return []
  return fillSeries(usage.value.from, usage.value.to, usage.value.bucket, usage.value.series).map((point) => ({
    label: usage.value?.bucket === 'hour' ? splitWhen(point.start).time || splitWhen(point.start).date : splitWhen(point.start).date,
    value: point.requests,
  }))
})
const mix = computed(() => {
  const summary = usage.value?.summary
  const total = summary?.total || 0
  const part = (value: number) => (total ? `${Math.round((value / total) * 100)}%` : '0%')
  return [
    { key: 'good', label: 'Succeeded', value: summary?.succeeded || 0, width: part(summary?.succeeded || 0) },
    { key: 'bad', label: 'Failed', value: summary?.failed || 0, width: part(summary?.failed || 0) },
    { key: 'warn', label: 'Canceled', value: summary?.canceled || 0, width: part(summary?.canceled || 0) },
    { key: 'muted', label: 'Rejected', value: summary?.rejected || 0, width: part(summary?.rejected || 0) },
  ]
})

function load() {
  return runLoad(async () => {
  if (props.scenario === 'loading') {
    stats.value = null
    usage.value = null
    latest.value = []
    return
  }
  error.value = ''
  const range = presetRange(preset.value)
  try {
    const [nextStats, nextUsage, page] = await Promise.all([
      props.service.stats(props.scenario),
      props.service.usage(range, props.scenario),
      props.service.requests({
        id: '', customer: '', group: '', status: '', model: '', account: '', effort: '', code: '', q: '',
        from: range.from, to: range.to, limit: 10, cursor: '',
      }, props.scenario),
    ])
    stats.value = nextStats
    usage.value = nextUsage
    latest.value = page.requests
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Request failed'
    notify(error.value)
  }
  })
}
watch(() => props.scenario, () => { void load() }, { immediate: true })
watch(refreshTick, () => { void load() })
watch(preset, () => { void load() })
</script>

<template>
  <div>
    <PageHeader kicker="Key shop" title="Dashboard" copy="Lifetime key health, plus usage for the selected range." />
    <p v-if="error" class="alert mb-4" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" :disabled="loading" @click="load">{{ loading ? 'Loading...' : 'Retry' }}</button></p>
    <p v-if="loading && !stats" class="panel" role="status">Loading dashboard…</p>
    <template v-else-if="stats">
      <div class="metric-grid">
        <MetricCard label="Total keys" :value="formatNumber(stats.keysTotal)" note="Stored" />
        <MetricCard label="Active" :value="formatNumber(stats.keysActive)" note="Allowed to call" />
        <MetricCard label="Tokens used" :value="formatNumber(stats.tokensUsedTotal)" note="Lifetime total" />
        <MetricCard label="Admitted" :value="formatNumber(stats.requestsAdmitted)" note="Counts toward the cap" />
      </div>
      <div class="quiet-grid">
        <MetricCard quiet label="Succeeded" :value="formatNumber(stats.requestsSucceeded)" />
        <MetricCard quiet label="Failed" :value="formatNumber(stats.requestsFailed)" />
        <MetricCard quiet label="Canceled" :value="formatNumber(stats.requestsCanceled)" />
        <MetricCard quiet label="Rejected" :value="formatNumber(stats.requestsRejected)" />
        <MetricCard quiet label="At request cap" :value="formatNumber(stats.keysRequestLimited)" />
        <MetricCard quiet label="Expiring" :value="formatNumber(stats.expiringWithin7d)" />
      </div>

      <section class="panel mt-5">
        <div class="flex flex-col justify-between gap-3 sm:flex-row sm:items-end">
          <div>
            <h2 class="section-title">Usage</h2>
            <p class="section-copy">Requests over time. Empty hours are filled so the shape stays readable.</p>
          </div>
          <div class="preset-row" role="group" aria-label="Usage range">
            <button v-for="item in presets" :key="item" type="button" class="preset" :aria-pressed="preset === item" @click="preset = item">{{ item }}</button>
          </div>
        </div>
        <AreaChart class="mt-4" :points="chartPoints" :label="`Requests per ${usage?.bucket || 'bucket'}`" />
        <div v-if="usage" class="quiet-grid">
          <MetricCard quiet label="In range" :value="formatNumber(usage.summary.total)" note="Requests" />
          <MetricCard quiet label="Error rate" :value="formatPercent(usage.summary.errorRate)" note="Failed / finished" />
          <MetricCard quiet label="Latency p50" :value="formatMs(usage.latencyP50)" :note="`p95 ${formatMs(usage.latencyP95)}`" />
          <MetricCard quiet label="TTFT p50" :value="formatMs(usage.ttftP50)" :note="`p95 ${formatMs(usage.ttftP95)}`" />
          <MetricCard quiet label="Cache hit" :value="formatPercent(usage.cacheHitRatio)" note="Cached / input" />
        </div>
        <div class="mix mt-4" role="img" :aria-label="mix.map((item) => `${item.label} ${item.value}`).join(', ')">
          <i v-for="item in mix" :key="item.key" :class="`mix-${item.key}`" :style="{ width: item.width }" />
        </div>
        <div class="mt-2 flex flex-wrap gap-3 text-xs text-[var(--ink-soft)]">
          <span v-for="item in mix" :key="item.key">{{ item.label }} {{ formatNumber(item.value) }}</span>
        </div>
      </section>

      <div class="rank-grid mt-5">
        <RankList title="Top models" :rows="usage?.topModels || []" />
        <RankList title="Top keys" :rows="usage?.topKeys || []" />
        <RankList title="Top customers" :rows="usage?.topCustomers || []" />
        <RankList title="Top accounts" :rows="usage?.topAccounts || []" />
      </div>

      <section class="panel mt-5">
        <div class="mb-3 flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 class="section-title">Latest requests</h2>
            <p class="section-copy">Ten newest rows in this range.</p>
          </div>
          <a class="text-link" href="#/requests">Open request log</a>
        </div>
        <div class="table-shell only-desktop">
          <div class="table-scroll">
            <table class="data-table fit">
              <caption class="sr-only">Latest requests</caption>
              <thead><tr><th>Time</th><th>Model</th><th>Key</th><th>Status</th></tr></thead>
              <tbody>
                <tr v-for="row in latest" :key="row.requestId">
                  <td><a class="text-link" href="#/requests">{{ splitWhen(row.requestedAt).date }} {{ splitWhen(row.requestedAt).time }}</a></td>
                  <td class="num text-xs">{{ row.model }}</td>
                  <td>{{ row.keyLabel || row.keyId }}<div class="sub">{{ row.customer || '—' }}</div></td>
                  <td><StatusChip :value="row.status" /></td>
                </tr>
                <tr v-if="!latest.length"><td colspan="4" class="py-8 text-center text-[var(--ink-soft)]">No requests in this range.</td></tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="card-list only-mobile">
          <a v-for="row in latest" :key="row.requestId" class="record text-link-card" href="#/requests">
            <div class="flex items-start justify-between gap-3"><strong class="num truncate text-xs">{{ row.model }}</strong><StatusChip :value="row.status" /></div>
            <p class="sub">{{ row.keyLabel || row.keyId }} · {{ row.customer || '—' }}</p>
          </a>
          <p v-if="!latest.length" class="record text-center text-sm text-[var(--ink-soft)]">No requests in this range.</p>
        </div>
      </section>
    </template>
  </div>
</template>
