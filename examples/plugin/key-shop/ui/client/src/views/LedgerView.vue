<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppModal from '@/components/ui/AppModal.vue'
import Icon from '@/components/ui/Icon.vue'
import ModelUsage from '@/components/ModelUsage.vue'
import Pager from '@/components/ui/Pager.vue'
import StatusChip from '@/components/ui/StatusChip.vue'
import { copyText, formatNumber, pageRange, rejectionLabel, splitWhen } from '@/lib/format'
import type { ClientService } from '@/mock/service'
import type { ClientSnapshot, RequestRow, Scenario, ThemeName } from '@/types'

const props = withDefaults(defineProps<{ service: ClientService; scenario?: Scenario; theme: ThemeName }>(), { scenario: 'ready' })
const emit = defineEmits<{ logout: []; toggleTheme: [] }>()
const snapshot = ref<ClientSnapshot | null>(null)
const error = ref('')
const page = ref(1)
const detail = ref<RequestRow | null>(null)
const toast = ref('')
const fetching = ref(false)
const scrolled = ref(false)
const modelsOpen = ref(false)
let toastTimer: ReturnType<typeof setTimeout> | undefined

const visible = computed(() => snapshot.value?.requests ?? [])
const requestTotal = computed(() => snapshot.value?.requestTotal ?? 0)
const requestHasMore = computed(() => Boolean(snapshot.value?.requestHasMore))
const cached = computed(() => visible.value.reduce((sum, row) => sum + row.cached, 0))
const unrestricted = computed(() => snapshot.value?.modelAccessMode === 'full' && snapshot.value.models.length === 0)
const modelUsage = computed(() => snapshot.value?.modelUsage ?? [])
const modelUsageSince = computed(() => snapshot.value?.modelUsageSince ?? '')
const initials = computed(() => {
  const words = (snapshot.value?.key.label || 'Key').trim().split(/\s+/)
  return words.slice(0, 2).map((word) => word.charAt(0).toUpperCase()).join('')
})
const identityDates = computed(() => {
  const key = snapshot.value?.key
  if (!key) return []
  return [
    { label: 'Created', value: key.createdAt, icon: 'calendar', empty: '—' },
    { label: 'Expires', value: key.expiresAt, icon: 'hourglass', empty: 'Never' },
    { label: 'Last used', value: key.lastUsedAt, icon: 'clock', empty: 'Not yet' },
  ]
})
const tallies = computed(() => {
  const key = snapshot.value?.key
  if (!key) return []
  return [
    { label: 'Recorded', value: key.requestCount, tone: '' },
    { label: 'Admitted', value: key.requestQuotaUsed, tone: 'tone-accent' },
    { label: 'Succeeded', value: key.requestSuccessCount, tone: 'tone-good' },
    { label: 'Failed', value: key.requestFailureCount, tone: 'tone-bad' },
    { label: 'Canceled', value: key.requestCanceledCount, tone: 'tone-warn' },
    { label: 'Rejected', value: key.requestRejectedCount, tone: '' },
  ]
})
const quotas = computed(() => {
  const key = snapshot.value?.key
  if (!key) return []
  return [
    {
      id: 'tokens',
      title: 'Token limit',
      used: key.tokensUsed,
      limit: key.tokenLimit,
      reached: false,
      unit: 'tokens',
      note: 'Billed tokens are input plus output. Reasoning and cached tokens are not added again.',
    },
    {
      id: 'requests',
      title: 'Request limit',
      used: key.requestQuotaUsed,
      limit: key.requestLimit,
      reached: key.requestLimitReached,
      unit: 'requests',
      note: 'Admitted calls count immediately, including failures and cancellations.',
    },
  ]
})

function ratio(used: number, limit: number) {
  if (!limit) return 1
  return Math.min(1, Math.max(0, used / limit))
}
function pct(used: number, limit: number) {
  if (!limit) return null
  const value = (used / limit) * 100
  if (value > 0 && value < 1) return '<1'
  return String(Math.min(100, Math.round(value)))
}
function tone(used: number, limit: number, reached = false) {
  if (!limit) return 'free'
  if (reached || used >= limit) return 'bad'
  if (used / limit >= 0.8) return 'warn'
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
    error.value = err instanceof Error ? err.message : 'Unable to open workspace.'
  } finally {
    fetching.value = false
  }
}
function refresh() {
  if (page.value === 1) void load()
  else page.value = 1
}
function notify(message: string) {
  toast.value = message
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toast.value = '' }, 2200)
}
async function copy(value: string) {
  notify((await copyText(value)) ? 'Endpoint copied' : 'Copy failed. Select the URL and copy it manually.')
}

// Passive, rAF-throttled scroll listener keeps the sticky bar cheap.
let ticking = false
function onScroll() {
  if (ticking) return
  ticking = true
  requestAnimationFrame(() => {
    scrolled.value = window.scrollY > 4
    ticking = false
  })
}
onMounted(() => {
  window.addEventListener('scroll', onScroll, { passive: true })
  onScroll()
})
onBeforeUnmount(() => {
  window.removeEventListener('scroll', onScroll)
  clearTimeout(toastTimer)
})

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
    <header class="topbar" :class="{ 'is-scrolled': scrolled }">
      <div class="shell topbar-inner">
        <div class="brand">
          <span class="brand-mark" aria-hidden="true"><Icon name="key" :size="16" /></span>
          <span class="brand-name">Key workspace</span>
          <span v-if="snapshot" class="key-pill num hide-xs"><i class="dot" aria-hidden="true" /><span class="truncate">{{ snapshot.key.display }}</span></span>
        </div>
        <div class="topbar-actions">
          <AppButton kind="ghost" class="icon-btn" :aria-label="theme === 'dark' ? 'Use light theme' : 'Use dark theme'" @click="emit('toggleTheme')">
            <Icon :name="theme === 'dark' ? 'sun' : 'moon'" />
          </AppButton>
          <AppButton aria-label="Switch key" @click="emit('logout')"><Icon name="logout" /><span class="hide-xs">Switch key</span></AppButton>
        </div>
      </div>
    </header>

    <main id="content" class="shell page">
      <div v-if="error" class="alert mb-4" role="alert">
        <span class="alert-body"><Icon name="alert" />{{ error }}</span>
        <button class="btn btn-sm btn-danger" type="button" :disabled="fetching" @click="load">Retry</button>
      </div>

      <!-- Loading skeleton: same footprint as the real layout to avoid layout shift -->
      <div v-if="!snapshot && !error" class="stack" aria-busy="true" aria-label="Loading workspace">
        <div class="hero">
          <div class="panel grid gap-4">
            <div class="flex items-center gap-3"><div class="skel h-[52px] w-[52px] rounded-2xl" /><div class="grid flex-1 gap-2"><div class="skel h-6 w-1/2" /><div class="skel h-4 w-1/3" /></div></div>
            <div class="skel h-20 w-full rounded-xl" />
            <div class="skel h-11 w-full rounded-xl" />
          </div>
          <div class="panel grid gap-6">
            <div class="grid gap-3"><div class="skel h-5 w-1/3" /><div class="skel h-2 w-full" /><div class="skel h-4 w-2/3" /></div>
            <div class="grid gap-3"><div class="skel h-5 w-1/3" /><div class="skel h-2 w-full" /><div class="skel h-4 w-2/3" /></div>
          </div>
        </div>
        <div class="stats"><div v-for="n in 6" :key="n" class="stat"><div class="skel h-4 w-2/3" /><div class="skel mt-2 h-6 w-1/2" /></div></div>
        <div class="panel grid gap-4">
          <div class="grid gap-2"><div class="skel h-5 w-1/3" /><div class="skel h-4 w-1/2" /></div>
          <div class="grid gap-3"><div v-for="n in 4" :key="n" class="skel h-6 w-full" /></div>
        </div>
      </div>

      <div v-if="snapshot" class="stack">
        <!-- Identity + quota -->
        <section class="panel hero-panel" aria-label="Key overview">
          <article class="identity">
            <div class="identity-top">
              <div class="avatar" aria-hidden="true">{{ initials }}</div>
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <h1 class="h-display">{{ snapshot.key.label }}</h1>
                  <StatusChip :value="snapshot.key.status" />
                </div>
                <p class="num quiet mt-1 truncate text-sm">{{ snapshot.key.display }}</p>
              </div>
            </div>

            <dl class="facts m-0">
              <div v-for="item in identityDates" :key="item.label" class="fact">
                <span class="fact-icon" aria-hidden="true"><Icon :name="item.icon" :size="15" /></span>
                <div class="fact-body">
                  <dt class="fact-label">{{ item.label }}</dt>
                  <dd class="m-0 fact-value">{{ item.value ? splitWhen(item.value).date : item.empty }}</dd>
                  <dd v-if="item.value" class="m-0 fact-sub">{{ splitWhen(item.value).time }}</dd>
                </div>
              </div>
            </dl>

            <div>
              <div class="eyebrow mb-2">Base URL</div>
              <div class="endpoint">
                <Icon name="link" class="flex-none text-[var(--ink-faint)]" />
                <code class="num" :title="snapshot.baseUrl">{{ snapshot.baseUrl }}</code>
                <AppButton small kind="primary" aria-label="Copy base URL" @click="copy(snapshot.baseUrl)"><Icon name="copy" :size="14" /><span class="hide-xs">Copy</span></AppButton>
              </div>
            </div>
          </article>

          <article class="quota-card" aria-label="Quota">
            <div v-for="q in quotas" :key="q.id" class="quota">
              <div class="quota-head">
                <div class="quota-title">
                  <h2>{{ q.title }}</h2>
                  <span class="num quota-used">{{ formatNumber(q.used) }} / {{ q.limit ? formatNumber(q.limit) : '∞' }}</span>
                </div>
                <div class="quota-pct" :class="{ 'tone-bad': tone(q.used, q.limit, q.reached) === 'bad', 'tone-warn': tone(q.used, q.limit, q.reached) === 'warn' }">
                  <template v-if="pct(q.used, q.limit) !== null">{{ pct(q.used, q.limit) }}<small>%</small></template>
                  <template v-else><span class="quota-unlimited">Unlimited</span></template>
                </div>
              </div>
              <div
                class="meter"
                :class="tone(q.used, q.limit, q.reached)"
                role="progressbar"
                :aria-label="q.title"
                aria-valuemin="0"
                :aria-valuemax="q.limit || undefined"
                :aria-valuenow="q.limit ? Math.min(q.used, q.limit) : undefined"
                :aria-valuetext="q.limit ? `${formatNumber(q.used)} of ${formatNumber(q.limit)} ${q.unit}` : 'Unlimited'"
              ><i :style="{ '--fill': ratio(q.used, q.limit) }" /></div>
              <div class="quota-meta">
                <span>{{ q.limit ? `${formatNumber(Math.max(0, q.limit - q.used))} ${q.unit} left` : `No ${q.unit} cap` }}</span>
              </div>
              <p class="quota-note">{{ q.note }}</p>
              <p v-if="q.reached" class="alert-inline m-0" role="alert"><Icon name="alert" :size="14" />Request limit reached. Ask the administrator to raise it.</p>
            </div>
          </article>
        </section>

        <!-- Totals -->
        <section aria-label="Usage totals" >
          <dl class="stats">
            <div v-for="item in tallies" :key="item.label" class="stat">
              <dt :class="item.tone"><i aria-hidden="true" />{{ item.label }}</dt>
              <dd>{{ formatNumber(item.value) }}</dd>
            </div>
          </dl>
          <p class="stats-foot">
            <span><strong>{{ formatNumber(snapshot.key.tokensUsed) }}</strong> lifetime tokens</span>
            <span><strong>{{ formatNumber(cached) }}</strong> cached on this page</span>
            <span>Failed, canceled, and rejected calls still count toward the request cap.</span>
          </p>
        </section>

        <!-- Usage by model -->
        <ModelUsage :rows="modelUsage" :since="modelUsageSince" />

        <!-- Models -->
        <section class="panel" aria-labelledby="models-title">
          <div class="panel-head">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <h2 id="models-title" class="h-section">Available models</h2>
                <span class="badge num">{{ snapshot.models.length || (unrestricted ? 'All' : 0) }}</span>
                <AppButton v-if="snapshot.models.length > 12" small kind="ghost" :aria-expanded="modelsOpen" aria-controls="model-well" @click="modelsOpen = !modelsOpen">
                  {{ modelsOpen ? 'Show less' : 'Show all' }}
                </AppButton>
              </div>
              <p class="copy">
                <template v-if="snapshot.group">Group <span class="badge badge-accent ml-1"><Icon name="layers" :size="12" />{{ snapshot.group.name }}</span></template>
                <template v-else>No model group is assigned to this key.</template>
              </p>
            </div>
          </div>
          <div v-if="snapshot.models.length" id="model-well" class="model-well" :class="{ open: modelsOpen }" tabindex="0" aria-label="Model list">
            <span v-for="model in snapshot.models" :key="model" class="model-chip">{{ model }}</span>
          </div>
          <p v-else-if="unrestricted" class="note"><Icon name="check" class="tone-good" />Every model on this server is available.</p>
          <p v-else class="note"><Icon name="alert" class="tone-warn" />No models are allowed for this key.</p>
        </section>

        <!-- Requests -->
        <section aria-labelledby="requests-title">
          <div class="section-head">
            <div class="min-w-0">
              <h2 id="requests-title" class="h-page">Recent requests</h2>
              <p class="copy">Prompts and response content are never stored.</p>
            </div>
            <AppButton :disabled="fetching" aria-label="Refresh requests" @click="refresh">
              <Icon name="refresh" :class="{ spin: fetching }" /><span class="hide-xs">Refresh</span>
            </AppButton>
          </div>

          <div class="table-shell only-desktop" :class="{ 'opacity-60': fetching }" style="transition: opacity 0.15s">
            <div class="table-scroll">
              <table class="data-table">
                <caption class="sr-only">Recent requests</caption>
                <thead>
                  <tr><th scope="col">Time</th><th scope="col">Model</th><th scope="col">Transport</th><th scope="col">Status</th><th scope="col" class="col-ttft">TTFT</th><th scope="col" class="col-tokens">Tokens</th><th scope="col" class="col-go"><span class="sr-only">Details</span></th></tr>
                </thead>
                <tbody>
                  <tr v-for="row in visible" :key="row.requestId" class="row" @click="detail = row">
                    <td class="whitespace-nowrap"><time :datetime="row.requestedAt">{{ splitWhen(row.requestedAt).date }}</time><div class="sub">{{ splitWhen(row.requestedAt).time }}</div></td>
                    <td class="num max-w-60 truncate text-[13px]" :title="row.model">{{ row.model }}</td>
                    <td class="quiet text-[13px]">{{ row.transport }}</td>
                    <td><StatusChip :value="row.status" /></td>
                    <td class="num quiet col-ttft text-[13px]">{{ row.ttftMs ? `${formatNumber(row.ttftMs)} ms` : '—' }}</td>
                    <td class="num col-tokens text-right text-[13px] font-semibold">{{ formatNumber(row.billed) }}</td>
                    <td class="col-go text-right">
                      <button type="button" class="row-go" :aria-label="`Details for ${row.model} at ${splitWhen(row.requestedAt).full}`" @click.stop="detail = row"><Icon name="chevron-right" /></button>
                    </td>
                  </tr>
                  <tr v-if="!visible.length">
                    <td colspan="7">
                      <div class="empty"><span class="empty-icon"><Icon name="inbox" :size="20" /></span><strong>No requests yet</strong><span>Calls made with this key will show up here.</span></div>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <div class="only-mobile records" :class="{ 'opacity-60': fetching }">
            <button v-for="row in visible" :key="row.requestId" type="button" class="record" @click="detail = row">
              <span class="record-top">
                <strong class="num min-w-0 truncate text-[13px]">{{ row.model }}</strong>
                <StatusChip :value="row.status" />
              </span>
              <span class="record-meta">
                <span>{{ splitWhen(row.requestedAt).date }} · {{ splitWhen(row.requestedAt).time }}</span>
                <span><b class="num">{{ formatNumber(row.billed) }}</b> tokens</span>
                <span v-if="row.ttftMs">TTFT <b class="num">{{ formatNumber(row.ttftMs) }} ms</b></span>
              </span>
            </button>
            <div v-if="!visible.length" class="panel empty"><span class="empty-icon"><Icon name="inbox" :size="20" /></span><strong>No requests yet</strong><span>Calls made with this key will show up here.</span></div>
          </div>

          <Pager :note="pageRange(page, visible.length, requestTotal, service.pageSize)" :page="page" :has-more="requestHasMore" :disabled="fetching" @prev="page -= 1" @next="page += 1" />
        </section>
      </div>
    </main>

    <AppModal :open="Boolean(detail)" kicker="Request details" :title="detail?.model || 'Request'" @close="detail = null">
      <div v-if="detail">
        <div class="mb-3 flex flex-wrap items-center gap-2">
          <StatusChip :value="detail.status" />
          <span class="quiet text-[13px]">{{ splitWhen(detail.requestedAt).full }}</span>
        </div>
        <div class="detail-grid">
          <div v-for="[label, read] in facts" :key="label">
            <span>{{ label }}</span>
            <strong class="num">{{ read(detail) }}</strong>
          </div>
        </div>
      </div>
    </AppModal>

    <Teleport to="body">
      <Transition name="toast">
        <div v-if="toast" class="toast" role="status" aria-live="polite"><Icon name="check" :size="14" />{{ toast }}</div>
      </Transition>
    </Teleport>
  </div>
</template>
