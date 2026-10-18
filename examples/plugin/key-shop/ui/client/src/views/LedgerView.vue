<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppModal from '@/components/ui/AppModal.vue'
import Icon from '@/components/ui/Icon.vue'
import Pager from '@/components/ui/Pager.vue'
import StatusChip from '@/components/ui/StatusChip.vue'
import RequestCounts from '@/components/ui/RequestCounts.vue'
import { copyText, formatNumber, pageRange, quota, rejectionLabel, splitWhen } from '@/lib/format'
import type { ClientService } from '@/mock/service'
import type { ClientSnapshot, RequestRow, Scenario, ThemeName } from '@/types'

const props = withDefaults(defineProps<{ service: ClientService; scenario?: Scenario; theme: ThemeName }>(), { scenario: 'ready' })
const emit = defineEmits<{ logout: []; toggleTheme: [] }>()
const snapshot = ref<ClientSnapshot | null>(null)
const error = ref('')
const page = ref(1)
const detail = ref<RequestRow | null>(null)
const copied = ref('')
const fetching = ref(false)
const visible = computed(() => snapshot.value?.requests ?? [])
const requestTotal = computed(() => snapshot.value?.requestTotal ?? 0)
const requestHasMore = computed(() => Boolean(snapshot.value?.requestHasMore))
const cached = computed(() => visible.value.reduce((sum, row) => sum + row.cached, 0))
const identityDates = computed(() => {
  const key = snapshot.value?.key
  if (!key) return []
  return [
    { label: 'Created', value: key.createdAt },
    { label: 'Expires', value: key.expiresAt },
    { label: 'Last used', value: key.lastUsedAt },
  ]
})
const tallies = computed(() => {
  const key = snapshot.value?.key
  if (!key) return []
  return [
    { label: 'Recorded', value: key.requestCount },
    { label: 'Admitted', value: key.requestQuotaUsed },
    { label: 'Succeeded', value: key.requestSuccessCount },
    { label: 'Failed', value: key.requestFailureCount },
    { label: 'Canceled', value: key.requestCanceledCount },
    { label: 'Rejected', value: key.requestRejectedCount },
  ]
})

function percent(used: number, limit: number) {
  if (!limit) return 8
  return Math.min(100, Math.round((used / limit) * 100))
}
function tone(used: number, limit: number, reached = false) {
  if (reached || (limit && used >= limit)) return 'bad'
  if (limit && used / limit >= 0.8) return 'warn'
  return ''
}
async function load() {
  error.value = ''
  if (props.scenario === 'loading') {
    snapshot.value = null
    return
  }
  fetching.value = true
  try {
    snapshot.value = await props.service.load({ scenario: props.scenario, page: page.value })
  } catch (err) {
    if (!snapshot.value) snapshot.value = null
    error.value = err instanceof Error ? err.message : 'Unable to open workspace.'
  } finally {
    fetching.value = false
  }
}
function refresh() {
  if (page.value === 1) void load()
  else page.value = 1
}
async function copy(value: string) {
  copied.value = (await copyText(value)) ? 'Endpoint copied' : 'Select the endpoint and copy it manually.'
}
watch(() => props.scenario, () => { page.value = 1; void load() }, { immediate: true })
watch(page, () => { void load() })

const facts = [
  ['Request ID', (row: RequestRow) => row.requestId],
  ['Status', (row: RequestRow) => row.status],
  ['Reason', (row: RequestRow) => rejectionLabel(row.rejectionCode)],
  ['HTTP status', (row: RequestRow) => row.httpStatus ? String(row.httpStatus) : '—'],
  ['Transport', (row: RequestRow) => row.transport],
  ['Protocol', (row: RequestRow) => row.protocol],
  ['Latency', (row: RequestRow) => row.latencyMs ? `${formatNumber(row.latencyMs)} ms` : '—'],
  ['TTFT', (row: RequestRow) => row.ttftMs ? `${formatNumber(row.ttftMs)} ms` : '—'],
  ['Input tokens', (row: RequestRow) => formatNumber(row.input)],
  ['Output tokens', (row: RequestRow) => formatNumber(row.output)],
  ['Reasoning tokens', (row: RequestRow) => formatNumber(row.reasoning)],
  ['Cached tokens', (row: RequestRow) => formatNumber(row.cached)],
] as const
</script>

<template>
  <div>
    <header class="ledger-bar">
      <div class="min-w-0">
        <div class="word">Key workspace</div>
        <div class="num truncate text-xs text-[var(--ink-faint)]">{{ snapshot?.key.display || 'ck_····' }}</div>
      </div>
      <div class="flex gap-1">
        <AppButton class="icon-btn" :aria-label="theme === 'dark' ? 'Use light theme' : 'Use dark theme'" @click="emit('toggleTheme')"><Icon :name="theme === 'dark' ? 'sun' : 'moon'" /></AppButton>
        <AppButton @click="emit('logout')">Switch key</AppButton>
      </div>
    </header>
    <main id="content" class="page">
      <p v-if="error" class="alert mb-5" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" @click="load">Retry</button></p>
      <template v-if="snapshot">
        <section class="hero-split">
          <article class="panel hero-card">
            <p class="kicker">Signed in</p>
            <div class="flex flex-wrap items-start justify-between gap-3">
              <h1 class="display">{{ snapshot.key.label }}</h1>
              <StatusChip :value="snapshot.key.status" />
            </div>
            <p class="num quiet mt-2 text-sm">{{ snapshot.key.display }}</p>
            <div class="mt-5 grid gap-3 sm:grid-cols-3">
              <div v-for="item in identityDates" :key="item.label" class="rounded-xl bg-[var(--bg-muted)] px-3 py-3">
                <span class="text-xs text-[var(--ink-faint)]">{{ item.label }}</span>
                <strong class="mt-1 block whitespace-nowrap text-sm">{{ item.value ? splitWhen(item.value).date : 'Never' }}</strong>
                <span v-if="item.value" class="block whitespace-nowrap text-xs text-[var(--ink-soft)]">{{ splitWhen(item.value).time }}</span>
              </div>
            </div>
            <div class="mt-auto pt-5">
              <div class="text-xs font-semibold text-[var(--ink-soft)]">Endpoint</div>
              <div class="mt-2 flex flex-wrap items-center gap-2">
                <code class="num text-sm">{{ snapshot.baseUrl }}</code>
                <AppButton small @click="copy(snapshot.baseUrl)">Copy</AppButton>
                <span class="text-xs text-[var(--ink-soft)]" role="status">{{ copied }}</span>
              </div>
            </div>
          </article>
          <article class="panel hero-card hero-quotas">
            <div>
              <div class="flex items-end justify-between gap-3">
                <h2 class="text-base font-semibold">Token limit</h2>
                <strong class="num text-sm">{{ quota(snapshot.key.tokensUsed, snapshot.key.tokenLimit) }}</strong>
              </div>
              <p class="section-copy">Billed tokens are input plus output. Reasoning and cached tokens are not added again.</p>
              <div class="meter mt-3" :class="tone(snapshot.key.tokensUsed, snapshot.key.tokenLimit)"><i :style="{ width: `${percent(snapshot.key.tokensUsed, snapshot.key.tokenLimit)}%` }" /></div>
              <div class="mt-2 text-xs text-[var(--ink-soft)]">{{ snapshot.key.tokenLimit ? `${formatNumber(Math.max(0, snapshot.key.tokenLimit - snapshot.key.tokensUsed))} remaining` : 'Unlimited tokens' }}</div>
            </div>
            <div>
              <div class="flex items-end justify-between gap-3">
                <h2 class="text-base font-semibold">Request limit</h2>
                <strong class="num text-sm">{{ quota(snapshot.key.requestQuotaUsed, snapshot.key.requestLimit) }}</strong>
              </div>
              <p class="section-copy">Admitted calls count immediately, including failures and cancellations.</p>
              <p v-if="snapshot.key.requestLimitReached" class="alert mt-3" role="alert">Request limit reached. Ask the administrator to raise the limit.</p>
              <div class="meter mt-3" :class="tone(snapshot.key.requestQuotaUsed, snapshot.key.requestLimit, snapshot.key.requestLimitReached)"><i :style="{ width: `${percent(snapshot.key.requestQuotaUsed, snapshot.key.requestLimit)}%` }" /></div>
              <div class="mt-2 text-xs">
                <RequestCounts :succeeded="snapshot.key.requestSuccessCount" :failed="snapshot.key.requestFailureCount" :canceled="snapshot.key.requestCanceledCount" :rejected="snapshot.key.requestRejectedCount" />
              </div>
            </div>
          </article>
        </section>

        <section class="panel ledger-strip mt-4" aria-label="Usage totals">
          <dl class="tally">
            <div v-for="item in tallies" :key="item.label">
              <dt>{{ item.label }}</dt>
              <dd>{{ formatNumber(item.value) }}</dd>
            </div>
          </dl>
          <p class="tally-foot">
            <span><strong>{{ formatNumber(snapshot.key.tokensUsed) }}</strong> lifetime tokens</span>
            <span><strong>{{ formatNumber(cached) }}</strong> cached on this page</span>
            <span>Failed, canceled, and rejected calls still count toward the request cap.</span>
          </p>
        </section>

        <section class="panel model-panel mt-4">
          <div class="flex items-center justify-between gap-3">
            <h2 class="text-base font-semibold">Available models</h2>
            <span class="num text-xs text-[var(--ink-soft)]">{{ snapshot.models.length }}</span>
          </div>
          <div v-if="snapshot.models.length" class="model-well" tabindex="0" aria-label="Available models">
            <span v-for="model in snapshot.models" :key="model" class="model-chip">{{ model }}</span>
          </div>
          <p v-else class="mt-3 text-sm text-[var(--ink-soft)]">Model list unavailable. Check the endpoint configuration.</p>
        </section>

        <section class="mt-4">
          <div class="mb-3 flex items-end justify-between gap-3">
            <div>
              <h2 class="section-title">Recent requests</h2>
              <p class="section-copy">Prompts and response content are never stored.</p>
            </div>
            <AppButton @click="refresh">Refresh</AppButton>
          </div>
          <div class="table-shell only-desktop">
            <div class="table-scroll">
              <table class="data-table">
                <caption class="sr-only">Recent requests</caption>
                <thead><tr><th>Time</th><th>Model</th><th>Transport</th><th>Status</th><th>TTFT</th><th class="text-right">Tokens</th><th></th></tr></thead>
                <tbody>
                  <tr v-for="row in visible" :key="row.requestId">
                    <td><time :datetime="row.requestedAt">{{ splitWhen(row.requestedAt).date }}</time><div class="sub">{{ splitWhen(row.requestedAt).time }}</div></td>
                    <td class="num max-w-56 truncate text-xs">{{ row.model }}</td>
                    <td>{{ row.transport }}</td>
                    <td><StatusChip :value="row.status" /></td>
                    <td class="num text-xs">{{ row.ttftMs ? `${formatNumber(row.ttftMs)} ms` : '—' }}</td>
                    <td class="num text-right text-xs">{{ formatNumber(row.billed) }}</td>
                    <td class="text-right"><AppButton small @click="detail = row">Details</AppButton></td>
                  </tr>
                  <tr v-if="!visible.length"><td colspan="7" class="py-10 text-center text-[var(--ink-soft)]">No requests yet.</td></tr>
                </tbody>
              </table>
            </div>
          </div>
          <div class="only-mobile grid gap-2.5">
            <article v-for="row in visible" :key="row.requestId" class="record">
              <div class="flex items-start justify-between gap-3"><strong class="num truncate text-xs">{{ row.model }}</strong><StatusChip :value="row.status" /></div>
              <div class="mt-3 grid grid-cols-2 gap-3">
                <div class="kv"><span>Time</span><strong>{{ splitWhen(row.requestedAt).date }}</strong></div>
                <div class="kv"><span>Billed</span><strong class="num">{{ formatNumber(row.billed) }}</strong></div>
              </div>
              <AppButton class="mt-3" small @click="detail = row">Details</AppButton>
            </article>
            <p v-if="!visible.length" class="record text-center text-sm text-[var(--ink-soft)]">No requests yet.</p>
          </div>
          <Pager :note="pageRange(page, visible.length, requestTotal, service.pageSize)" :page="page" :has-more="requestHasMore" :disabled="fetching" @prev="page -= 1" @next="page += 1" />
        </section>
      </template>
    </main>
    <AppModal :open="Boolean(detail)" kicker="Request details" :title="detail?.model || 'Request'" @close="detail = null">
      <div v-if="detail" class="mt-4 grid gap-2 sm:grid-cols-2">
        <div v-for="[label, read] in facts" :key="label" class="rounded-xl bg-[var(--bg-muted)] p-3">
          <span class="text-[11px] font-semibold text-[var(--ink-faint)]">{{ label }}</span>
          <strong class="num mt-1 block break-all text-sm">{{ read(detail) }}</strong>
        </div>
      </div>
      <div class="mt-4 flex justify-end"><AppButton @click="detail = null">Close</AppButton></div>
    </AppModal>
  </div>
</template>
