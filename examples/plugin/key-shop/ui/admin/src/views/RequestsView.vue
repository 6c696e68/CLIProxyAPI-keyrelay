<script setup lang="ts">
import { computed, inject, onBeforeUnmount, ref, watch } from 'vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import FilterBar from '@/components/FilterBar.vue'
import GroupSelect from '@/components/GroupSelect.vue'
import PageHeader from '@/components/PageHeader.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppDateTime from '@/components/ui/AppDateTime.vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import AppSwitch from '@/components/ui/AppSwitch.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import Pager from '@/components/ui/Pager.vue'
import StatusChip from '@/components/ui/StatusChip.vue'
import { notifyKey, refreshKey } from '@/composables/useToast'
import { toLocalInput } from '@/lib/datetime'
import { formatMs, formatNumber, formatTok, pageRange, rejectionLabel, shortenMiddle, splitWhen } from '@/lib/format'
import { presetRange, type RangePreset } from '@/lib/range'
import type { AdminService, ModelGroup, RequestPage, RequestRow, Scenario } from '@/types'

const props = withDefaults(defineProps<{ service: AdminService; scenario?: Scenario }>(), { scenario: 'ready' })
const notify = inject(notifyKey, () => {})
const refreshTick = inject(refreshKey, ref(0))
const groups = ref<ModelGroup[]>([])
const pageData = ref<RequestPage | null>(null)
const error = ref('')
const loading = ref(false)
const preset = ref<RangePreset | 'custom'>('7d')
const presets = ['1h', '24h', '7d', '30d', 'all', 'custom'] as const
const customFrom = ref(toLocalInput(new Date(Date.now() - 86_400_000)))
const customTo = ref(toLocalInput(new Date()))
const filters = ref({ id: '', customer: '', group: '', status: '', model: '', account: '', effort: '', code: '', q: '' })
const cursors = ref<string[]>([''])
const page = ref(1)
const auto = ref(false)
const detail = ref<RequestRow | null>(null)
let timer = 0
let refreshTimer = 0
let loadGeneration = 0
let keepCursorError = false

const rows = computed(() => pageData.value?.requests ?? [])
const summary = computed(() => pageData.value?.summary)
const statusOptions = [
  { value: '', label: 'All statuses' },
  { value: 'succeeded', label: 'Succeeded' },
  { value: 'failed', label: 'Failed' },
  { value: 'rejected', label: 'Rejected' },
  { value: 'canceled', label: 'Canceled' },
  { value: 'pending', label: 'Pending' },
]
const effortOptions = [
  { value: '', label: 'Any effort' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
  { value: 'xhigh', label: 'xhigh' },
  { value: 'max', label: 'Max' },
]

function range() {
  if (preset.value === 'custom') {
    return {
      from: customFrom.value ? new Date(customFrom.value).toISOString() : '',
      to: customTo.value ? new Date(customTo.value).toISOString() : '',
    }
  }
  return presetRange(preset.value)
}
async function load() {
  const generation = ++loadGeneration
  if (props.scenario === 'loading') {
    pageData.value = null
    return
  }
  const cursor = page.value <= 1 ? '' : cursors.value[page.value - 1] || ''
  if (page.value > 1 && !cursor) {
    error.value = 'Unable to load requests.'
    notify(error.value)
    keepCursorError = true
    page.value = 1
    return
  }
  loading.value = true
  if (keepCursorError) keepCursorError = false
  else error.value = ''
  const bounds = range()
  try {
    const [next, nextGroups] = await Promise.all([
      props.service.requests({
        ...filters.value,
        from: bounds.from,
        to: bounds.to,
        limit: props.service.pageSize,
        cursor,
      }, props.scenario),
      props.service.groups(props.scenario),
    ])
    if (generation !== loadGeneration) return
    if (next.nextCursor) cursors.value[page.value] = next.nextCursor
    pageData.value = next
    groups.value = nextGroups
  } catch (err) {
    if (generation !== loadGeneration) return
    error.value = err instanceof Error ? err.message : 'Request failed'
    notify(error.value)
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}
function reset() {
  cursors.value = ['']
  if (page.value !== 1) page.value = 1
  else void load()
}
function schedule() {
  window.clearTimeout(timer)
  timer = window.setTimeout(reset, 250)
}
watch(() => props.scenario, () => reset(), { immediate: true })
watch(refreshTick, () => { void load() })
watch(page, () => { void load() })
watch(filters, schedule, { deep: true })
watch(preset, reset)
watch([customFrom, customTo], () => { if (preset.value === 'custom') reset() })
watch(auto, (value) => {
  window.clearInterval(refreshTimer)
  if (value) refreshTimer = window.setInterval(() => { void load() }, 15_000)
})
onBeforeUnmount(() => {
  window.clearTimeout(timer)
  window.clearInterval(refreshTimer)
})

function open(row: RequestRow) {
  detail.value = row
}
function onRowKey(event: KeyboardEvent, row: RequestRow) {
  if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    open(row)
  }
}
</script>

<template>
  <div>
    <PageHeader kicker="Activity" title="Request log" copy="Each row is one customer call. Account is the upstream auth id for that call.">
      <AppSwitch :on="auto" text="Auto" label="Auto-refresh every 15 seconds" @toggle="auto = !auto" />
      <AppButton @click="load">Refresh</AppButton>
    </PageHeader>
    <p v-if="error" class="alert mb-4" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" @click="load">Retry</button></p>
    <section class="panel">
      <div class="preset-row mb-3" role="group" aria-label="Time range">
        <button v-for="item in presets" :key="item" type="button" class="preset" :aria-pressed="preset === item" @click="preset = item">{{ item }}</button>
      </div>
      <div v-if="preset === 'custom'" class="mb-3 grid gap-2 sm:grid-cols-2">
        <AppDateTime v-model="customFrom" />
        <AppDateTime v-model="customTo" />
      </div>
      <FilterBar>
        <AppTextInput v-model="filters.q" placeholder="Search id, model, key, customer" />
        <AppTextInput v-model="filters.id" placeholder="Key id" />
        <AppTextInput v-model="filters.customer" placeholder="Customer" />
        <GroupSelect v-model="filters.group" :groups="groups" include-all label="Group" />
        <AppTextInput v-model="filters.model" placeholder="Exact model" />
        <AppSelect v-model="filters.status" :options="statusOptions" label="Status" />
        <AppTextInput v-model="filters.account" placeholder="Account" />
        <AppSelect v-model="filters.effort" :options="effortOptions" label="Effort" />
        <AppTextInput v-model="filters.code" placeholder="Rejection code" />
      </FilterBar>

      <div v-if="summary" class="summary-strip" aria-label="Filtered totals">
        <div><span>Requests</span><strong class="num">{{ formatNumber(summary.total) }}</strong></div>
        <div><span>Succeeded</span><strong class="num">{{ formatNumber(summary.succeeded) }}</strong></div>
        <div><span>Failed</span><strong class="num">{{ formatNumber(summary.failed) }}</strong></div>
        <div><span>Rejected</span><strong class="num">{{ formatNumber(summary.rejected) }}</strong></div>
        <div><span>Error rate</span><strong class="num">{{ summary.total ? `${(summary.errorRate * 100).toFixed(1)}%` : '—' }}</strong></div>
        <div><span>Avg latency</span><strong class="num">{{ formatMs(summary.avgLatencyMs) }}</strong></div>
        <div><span>Avg TTFT</span><strong class="num">{{ formatMs(summary.avgTtftMs) }}</strong></div>
        <div><span>Tokens</span><strong class="num">{{ formatNumber(summary.input) }} → {{ formatNumber(summary.output) }}</strong></div>
      </div>

      <p v-if="loading && !pageData" role="status">Loading requests…</p>
      <template v-else>
        <div class="table-shell only-desktop">
          <div class="table-scroll">
            <table class="data-table log">
              <caption class="sr-only">Request log</caption>
              <thead>
                <tr>
                  <th>Time</th><th>Model</th><th>Key</th><th>Account</th><th>Status</th><th>Latency</th><th class="text-right">Tok/s</th><th class="text-right">Tokens</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in rows" :key="row.requestId" class="clickable" tabindex="0" @click="open(row)" @keydown="onRowKey($event, row)">
                  <td><time class="block" :datetime="row.requestedAt">{{ splitWhen(row.requestedAt).date }}</time><span class="sub">{{ splitWhen(row.requestedAt).time }}</span></td>
                  <td>
                    <span class="num clip block max-w-40 text-xs">{{ row.model }}</span>
                    <span v-if="row.effort" class="chip mt-1">{{ row.effort }}</span>
                  </td>
                  <td>
                    <strong class="block truncate">{{ row.keyLabel || row.keyId || '—' }}</strong>
                    <span class="sub block truncate">{{ row.customer || '—' }}</span>
                  </td>
                  <td class="num text-xs" :title="row.authId">{{ shortenMiddle(row.authId) }}</td>
                  <td>
                    <StatusChip :value="row.status" />
                    <span v-if="row.httpStatus" class="chip mt-1">{{ row.httpStatus }}</span>
                  </td>
                  <td class="num text-xs">{{ formatMs(row.latencyMs) }}<div class="sub">TTFT {{ formatMs(row.ttftMs) }}</div></td>
                  <td class="num text-right text-xs">{{ formatTok(row.tokPerSec) }}</td>
                  <td class="num text-right text-xs">
                    {{ formatNumber(row.input) }} → {{ formatNumber(row.output) }}
                    <div class="sub">cache {{ formatNumber(row.cached) }} · reason {{ formatNumber(row.reasoning) }}</div>
                  </td>
                </tr>
                <tr v-if="!rows.length"><td colspan="8" class="py-10 text-center text-[var(--ink-soft)]">No requests match these filters.</td></tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="card-list only-mobile">
          <article v-for="row in rows" :key="row.requestId" class="record" tabindex="0" @click="open(row)" @keydown="onRowKey($event, row)">
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <strong class="num block truncate text-xs">{{ row.model }}</strong>
                <span v-if="row.effort" class="chip mt-1">{{ row.effort }}</span>
              </div>
              <StatusChip :value="row.status" />
            </div>
            <div class="mt-3 grid grid-cols-2 gap-3">
              <div class="kv"><span>Time</span><strong>{{ splitWhen(row.requestedAt).date }}</strong></div>
              <div class="kv"><span>Key</span><strong>{{ row.keyLabel || '—' }}</strong></div>
              <div class="kv"><span>Customer</span><strong>{{ row.customer || '—' }}</strong></div>
              <div class="kv"><span>Account</span><strong class="num" :title="row.authId">{{ shortenMiddle(row.authId, 6, 4) }}</strong></div>
              <div class="kv"><span>Latency</span><strong class="num">{{ formatMs(row.latencyMs) }}</strong></div>
              <div class="kv"><span>Tok/s</span><strong class="num">{{ formatTok(row.tokPerSec) }}</strong></div>
              <div class="kv col-span-2"><span>Tokens</span><strong class="num">{{ formatNumber(row.input) }} → {{ formatNumber(row.output) }}</strong></div>
            </div>
          </article>
          <p v-if="!rows.length" class="record text-center text-sm text-[var(--ink-soft)]">No requests match these filters.</p>
        </div>
        <Pager :note="pageRange(page, rows.length, pageData?.total || 0, service.pageSize)" :page="page" :has-more="Boolean(pageData?.hasMore)" :disabled="loading" @prev="page -= 1" @next="page += 1" />
      </template>
    </section>

    <DetailDrawer :open="Boolean(detail)" title="Request" :kicker="detail?.requestId" @close="detail = null">
      <template v-if="detail">
        <div class="kv"><span>Key</span><strong>{{ detail.keyLabel || '—' }}</strong><span class="sub num">{{ detail.keyDisplay || detail.keyId }}</span></div>
        <div class="kv"><span>Customer</span><strong>{{ detail.customer || '—' }}</strong></div>
        <div class="kv"><span>Group</span><strong>{{ detail.groupName || '—' }}</strong></div>
        <div class="kv"><span>Model</span><strong class="num">{{ detail.model }}</strong></div>
        <div class="kv"><span>Requested</span><strong class="num">{{ detail.requestedModel || '—' }}</strong></div>
        <div class="kv"><span>Account</span><strong class="num" :title="detail.authId">{{ detail.authId || '—' }}</strong></div>
        <div class="kv"><span>Provider</span><strong>{{ detail.provider || '—' }}</strong></div>
        <div class="kv"><span>Auth index</span><strong class="num">{{ detail.authIndex || '—' }}</strong></div>
        <div class="kv"><span>Auth type</span><strong>{{ detail.authType || '—' }}</strong></div>
        <div class="kv"><span>Effort</span><strong>{{ detail.effort || '—' }}</strong></div>
        <div class="kv"><span>Tier</span><strong>{{ detail.serviceTier || '—' }}</strong></div>
        <div class="kv"><span>Protocol</span><strong>{{ detail.protocol || '—' }}</strong></div>
        <div class="kv"><span>Transport</span><strong>{{ detail.transport || '—' }}</strong></div>
        <div class="kv"><span>Status</span><strong>{{ detail.status }}<span v-if="detail.httpStatus"> · {{ detail.httpStatus }}</span></strong></div>
        <div class="kv"><span>Rejection</span><strong>{{ rejectionLabel(detail.rejectionCode) }}</strong></div>
        <div class="kv"><span>Latency</span><strong class="num">{{ formatMs(detail.latencyMs) }}</strong></div>
        <div class="kv"><span>TTFT</span><strong class="num">{{ formatMs(detail.ttftMs) }}</strong></div>
        <div class="kv"><span>Tok/s</span><strong class="num">{{ formatTok(detail.tokPerSec) }}</strong></div>
        <div class="kv"><span>Tokens</span><strong class="num">{{ formatNumber(detail.input) }} in · {{ formatNumber(detail.output) }} out</strong></div>
        <div class="kv"><span>Cache / reasoning</span><strong class="num">{{ formatNumber(detail.cached) }} / {{ formatNumber(detail.reasoning) }}</strong></div>
        <div class="kv"><span>Completed</span><strong>{{ detail.completedAt ? splitWhen(detail.completedAt).full : '—' }}</strong></div>
      </template>
    </DetailDrawer>
  </div>
</template>
