<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AdminShell from '@/components/AdminShell.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppCheckbox from '@/components/ui/AppCheckbox.vue'
import AppDateTime from '@/components/ui/AppDateTime.vue'
import AppField from '@/components/ui/AppField.vue'
import AppModal from '@/components/ui/AppModal.vue'
import AppNumberInput from '@/components/ui/AppNumberInput.vue'
import AppSegmented from '@/components/ui/AppSegmented.vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import AppSwitch from '@/components/ui/AppSwitch.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import Pager from '@/components/ui/Pager.vue'
import StatusChip from '@/components/ui/StatusChip.vue'
import { toLocalInput } from '@/lib/datetime'
import RequestCounts from '@/components/ui/RequestCounts.vue'
import { copyText, formatNumber, pageRange, quota, randomLabel, rejectionLabel, splitWhen } from '@/lib/format'
import { defaultCreateDraft, type AdminService } from '@/mock/service'
import type { AdminSnapshot, CreateDraft, EditDraft, KeyRecord, ModelEntry, PolicyMode, RequestRow, Scenario, ThemeName } from '@/types'

const props = withDefaults(defineProps<{ service: AdminService; scenario?: Scenario; theme: ThemeName }>(), { scenario: 'ready' })
const emit = defineEmits<{ logout: []; toggleTheme: [] }>()

const snapshot = ref<AdminSnapshot | null>(null)
const error = ref('')
const toast = ref('')
const query = ref('')
const filter = ref('')
const page = ref(1)
const requestPage = ref(1)
const selectedId = ref('')
const createOpen = ref(false)
const editOpen = ref(false)
const confirming = ref<KeyRecord | null>(null)
const statusTarget = ref<KeyRecord | null>(null)
const secrets = ref<string[]>([])
const policyMode = ref<PolicyMode>('full')
const policyModels = ref<ModelEntry[]>([])
const policyQuery = ref('')
const policyMessage = ref('')
const policyError = ref('')
const fetching = ref(false)
const policySynced = ref(false)
const createDraft = ref<CreateDraft>(defaultCreateDraft())
const editDraft = ref<EditDraft | null>(null)
let toastTimer = 0
let queryTimer = 0

const loading = computed(() => props.scenario === 'loading' || fetching.value)
const interactive = computed(() => !loading.value && props.scenario !== 'error')
const visibleKeys = computed(() => snapshot.value?.keys ?? [])
const requestSlice = computed(() => snapshot.value?.requests ?? [])
const keysTotal = computed(() => snapshot.value?.keysTotal ?? 0)
const requestTotal = computed(() => snapshot.value?.requestTotal ?? 0)
const requestHasMore = computed(() => Boolean(snapshot.value?.requestHasMore))
const keyHasMore = computed(() => page.value * props.service.pageSize < keysTotal.value)
const discovered = computed(() => (snapshot.value?.availableModels ?? []).filter((name) => name.toLowerCase().includes(policyQuery.value.trim().toLowerCase())))
const statusOptions = [
  { value: '', label: 'All statuses' },
  { value: 'active', label: 'Active' },
  { value: 'disabled', label: 'Disabled' },
  { value: 'expired', label: 'Expired' },
  { value: 'exhausted', label: 'Exhausted' },
]

function notify(message: string) {
  toast.value = message
  window.clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => { toast.value = '' }, 2200)
}
async function load() {
  error.value = ''
  if (props.scenario === 'loading') {
    snapshot.value = null
    return
  }
  fetching.value = true
  try {
    snapshot.value = await props.service.load({
      scenario: props.scenario,
      query: query.value,
      status: filter.value,
      page: page.value,
      selectedId: selectedId.value,
      requestPage: requestPage.value,
    })
    if (!policySynced.value) {
      policyMode.value = snapshot.value.policy.mode
      policyModels.value = snapshot.value.policy.models.map((item) => ({ ...item }))
      if (snapshot.value.policy.loaded) policySynced.value = true
    }
  } catch (err) {
    if (!snapshot.value) snapshot.value = null
    error.value = err instanceof Error ? err.message : 'Request failed'
  } finally {
    fetching.value = false
  }
}
watch(() => props.scenario, () => { page.value = 1; requestPage.value = 1; policySynced.value = false; void load() }, { immediate: true })
function refresh() {
  policySynced.value = false
  void load()
}
watch([page, requestPage, selectedId], () => { void load() })
watch([query, filter], () => {
  page.value = 1
  window.clearTimeout(queryTimer)
  queryTimer = window.setTimeout(() => { void load() }, 250)
})

function reasons(key: KeyRecord) {
  return [key.requestLimitReached ? 'Request limit' : '', key.tokenLimitReached ? 'Token limit' : ''].filter(Boolean).join(' · ')
}
function openEdit(key: KeyRecord) {
  selectedId.value = key.id
  editDraft.value = {
    id: key.id,
    label: key.label,
    customer: key.customer,
    note: key.note,
    status: key.status === 'disabled' ? 'disabled' : 'active',
    expiresLocal: key.expiresAt ? toLocalInput(new Date(key.expiresAt)) : toLocalInput(new Date(Date.now() + 7 * 86_400_000)),
    neverExpires: !key.expiresAt,
    tokenLimit: key.tokenLimit,
    requestLimit: key.requestLimit,
    unlimitedRequests: key.requestLimit === 0,
  }
  editOpen.value = true
  requestPage.value = 1
}
async function run(action: () => Promise<void>, message: string) {
  try {
    await action()
    await load()
    notify(message)
  } catch (err) {
    notify(err instanceof Error ? err.message : 'Request failed')
  }
}
async function savePolicy() {
  policyError.value = ''
  policyMessage.value = ''
  try {
    await props.service.savePolicy({ mode: policyMode.value, models: policyModels.value.filter((item) => item.name.trim()), loaded: true }, props.scenario)
    policyMessage.value = 'Model access policy saved.'
    await load()
  } catch (err) {
    policyError.value = err instanceof Error ? err.message : 'Policy could not be saved.'
  }
}
function addModel(name: string) {
  if (!policyModels.value.some((item) => item.name.toLowerCase() === name.toLowerCase())) policyModels.value.push({ name, alias: '' })
}
async function createKeys() {
  try {
    secrets.value = await props.service.create(createDraft.value, props.scenario)
    notify(`Created ${secrets.value.length} key${secrets.value.length === 1 ? '' : 's'}`)
    await load()
  } catch (err) {
    notify(err instanceof Error ? err.message : 'Create failed')
  }
}
async function saveEdit() {
  if (!editDraft.value) return
  if (!editDraft.value.neverExpires && !editDraft.value.expiresLocal) {
    notify('Choose an expiry or turn on Never expires.')
    return
  }
  await run(() => props.service.update(editDraft.value as EditDraft, props.scenario), 'Changes saved')
}
function primaryMetrics(data: AdminSnapshot) {
  return [
    ['Total keys', data.stats.keysTotal, 'Stored'],
    ['Active', data.stats.keysActive, 'Allowed to call'],
    ['Tokens used', formatNumber(data.stats.tokensUsedTotal), 'Lifetime total'],
    ['Admitted', formatNumber(data.stats.requestsAdmitted), 'Counts toward the cap'],
  ]
}
function secondaryMetrics(data: AdminSnapshot) {
  return [
    ['Succeeded', data.stats.requestsSucceeded],
    ['Failed', data.stats.requestsFailed],
    ['Canceled', data.stats.requestsCanceled],
    ['Rejected', data.stats.requestsRejected],
    ['At request cap', data.stats.keysRequestLimited],
    ['Expiring', data.stats.expiringWithin7d],
  ]
}
</script>

<template>
  <AdminShell :theme="theme" @refresh="refresh" @logout="emit('logout')" @toggle-theme="emit('toggleTheme')">
    <main id="content" class="content">
      <div class="mb-6 flex flex-col justify-between gap-4 md:flex-row md:items-end">
        <div>
          <p class="page-kicker">Key shop</p>
          <h1 class="page-title">Customer access</h1>
          <p class="page-copy">Issue keys, set limits, and inspect request outcomes. Secrets appear only after creation or rotation.</p>
        </div>
        <AppButton kind="primary" @click="createOpen = true">Create keys</AppButton>
      </div>

      <p v-if="error" class="alert mb-4" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" @click="load">Retry</button></p>

      <section id="overview" class="mb-5 scroll-mt-20">
        <div v-if="snapshot" class="grid gap-3">
          <div class="metric-grid">
            <article v-for="[label, value, note] in primaryMetrics(snapshot)" :key="label" class="panel metric">
              <span class="metric-label">{{ label }}</span>
              <strong class="metric-value num">{{ value }}</strong>
              <span class="metric-note">{{ note }}</span>
            </article>
          </div>
          <div class="grid grid-cols-2 gap-2 md:grid-cols-3 xl:grid-cols-6">
            <div v-for="[label, value] in secondaryMetrics(snapshot)" :key="label" class="quiet-metric">
              <span class="metric-label">{{ label }}</span>
              <strong class="num mt-1 block text-base">{{ formatNumber(Number(value)) }}</strong>
            </div>
          </div>
        </div>
      </section>

      <section id="policy" class="panel mb-5 scroll-mt-20">
        <div class="flex flex-col justify-between gap-3 sm:flex-row sm:items-end">
          <div>
            <h2 class="section-title">Customer model access</h2>
            <p class="section-copy">This policy applies only to ck_ customer keys. Find models uses the same /v1/models list as Available Models.</p>
          </div>
          <AppButton kind="primary" :disabled="loading || !interactive || !snapshot?.policy.loaded" @click="savePolicy">Save policy</AppButton>
        </div>
        <div class="mt-4 grid max-w-sm gap-2">
          <span class="field-label">Access mode</span>
          <AppSegmented :model-value="policyMode" :disabled="!interactive" label="Access mode" :options="[{ value: 'full', label: 'All models' }, { value: 'custom', label: 'Selected models' }]" @update:model-value="policyMode = $event as PolicyMode" />
        </div>
        <div v-if="policyMode === 'custom'" class="model-split mt-4">
          <div class="model-pane">
            <div class="mb-2 flex items-center justify-between"><strong>Find models</strong><span class="text-xs text-[var(--ink-soft)]">{{ snapshot?.availableModels.length || 0 }} available</span></div>
            <AppTextInput v-model="policyQuery" placeholder="Search model name" />
            <div class="model-scroll mt-2">
              <button v-for="name in discovered" :key="name" type="button" class="flex w-full items-center justify-between gap-3 rounded-lg px-2 py-2 text-left hover:bg-[var(--bg-muted)]" :disabled="policyModels.some((item) => item.name.toLowerCase() === name.toLowerCase())" @click="addModel(name)">
                <span class="num truncate text-xs">{{ name }}</span>
                <span class="text-xs text-[var(--ink-soft)]">{{ policyModels.some((item) => item.name.toLowerCase() === name.toLowerCase()) ? 'Added' : 'Add' }}</span>
              </button>
              <p v-if="!discovered.length" class="py-6 text-center text-sm text-[var(--ink-soft)]">No matching models.</p>
            </div>
          </div>
          <div class="model-pane">
            <div class="mb-2 flex items-center justify-between"><strong>Allowed models</strong><span class="text-xs text-[var(--ink-soft)]">{{ policyModels.length }} selected</span></div>
            <div class="model-scroll">
            <div v-for="(item, index) in policyModels" :key="`${item.name}-${index}`" class="mb-2 grid gap-2 md:grid-cols-[1fr_1fr_auto]">
              <AppTextInput :model-value="item.name" @update:model-value="policyModels[index] = { ...item, name: $event }" />
              <AppTextInput :model-value="item.alias" placeholder="Client alias" @update:model-value="policyModels[index] = { ...item, alias: $event }" />
              <AppButton kind="danger" small @click="policyModels.splice(index, 1)">Remove</AppButton>
            </div>
            <p v-if="!policyModels.length" class="py-6 text-center text-sm text-[var(--ink-soft)]">No models selected.</p>
            </div>
          </div>
        </div>
        <p v-if="policyError" class="alert mt-3" role="alert">{{ policyError }}</p>
        <p v-if="policyMessage" class="mt-3 text-sm text-[var(--good)]" role="status">{{ policyMessage }}</p>
      </section>

      <section id="inventory" class="panel mb-5 scroll-mt-20">
        <div class="mb-4 flex flex-col justify-between gap-3 sm:flex-row sm:items-end">
          <div>
            <h2 class="section-title">Key inventory</h2>
            <p class="section-copy">{{ formatNumber(keysTotal) }} matching keys.</p>
          </div>
        </div>
        <div class="mb-4 grid gap-2 md:grid-cols-[1fr_220px]">
          <AppTextInput v-model="query" placeholder="Search label, customer, or ID" />
          <AppSelect v-model="filter" :options="statusOptions" label="Filter by status" />
        </div>
        <template v-if="snapshot">
          <div class="table-shell only-desktop">
            <div class="table-scroll">
              <table class="data-table">
                <caption class="sr-only">Customer keys</caption>
                <thead>
                  <tr>
                    <th>Key</th><th>Customer</th><th>Status</th><th class="text-right">Requests</th><th>Usage</th><th>Created</th><th>Expires</th><th class="actions">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="key in visibleKeys" :key="key.id">
                    <td><strong class="num clip block max-w-44 text-[13px] font-medium">{{ key.label }}</strong><div class="sub num">{{ key.display }}</div></td>
                    <td>{{ key.customer || '—' }}</td>
                    <td><StatusChip interactive :value="key.status" @select="statusTarget = key" /><div v-if="reasons(key)" class="sub text-[var(--warn)]">{{ reasons(key) }}</div></td>
                    <td class="num text-right text-xs">{{ quota(key.requestQuotaUsed, key.requestLimit) }}<div class="sub"><RequestCounts :succeeded="key.requestSuccessCount" :failed="key.requestFailureCount" :canceled="key.requestCanceledCount" :rejected="key.requestRejectedCount" /></div></td>
                    <td class="num usage-cell text-xs">
                      <template v-if="key.tokenLimit">
                        <span>{{ formatNumber(key.tokensUsed) }}</span>
                        <span class="usage-limit">/ {{ formatNumber(key.tokenLimit) }}</span>
                      </template>
                      <template v-else>{{ formatNumber(key.tokensUsed) }} / ∞</template>
                    </td>
                    <td><time class="block text-sm" :datetime="key.createdAt">{{ splitWhen(key.createdAt).date }}</time><span class="sub">{{ splitWhen(key.createdAt).time }}</span></td>
                    <td><template v-if="key.expiresAt"><time class="block text-sm" :datetime="key.expiresAt">{{ splitWhen(key.expiresAt).date }}</time><span class="sub">{{ splitWhen(key.expiresAt).time }}</span></template><template v-else>Never</template></td>
                    <td class="actions">
                      <div class="action-stack">
                        <div class="flex gap-1">
                          <AppButton small @click="openEdit(key)">Edit</AppButton>
                          <AppButton small @click="run(async () => { secrets = [await service.rotate(key.id, scenario)]; createOpen = true }, 'Key rotated')">Rotate</AppButton>
                          <AppButton small kind="danger" @click="confirming = key">Delete</AppButton>
                        </div>
                      </div>
                    </td>
                  </tr>
                  <tr v-if="!visibleKeys.length"><td colspan="8" class="py-10 text-center text-[var(--ink-soft)]">No matching keys.</td></tr>
                </tbody>
              </table>
            </div>
          </div>
          <div class="card-list only-mobile">
            <article v-for="key in visibleKeys" :key="key.id" class="record">
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0"><strong class="block truncate">{{ key.label }}</strong><span class="num sub block">{{ key.display }}</span></div>
                <StatusChip interactive :value="key.status" @select="statusTarget = key" />
              </div>
              <div class="mt-3 grid grid-cols-2 gap-3">
                <div class="kv"><span>Customer</span><strong>{{ key.customer || '—' }}</strong></div>
                <div class="kv"><span>Usage</span><strong class="num usage-cell"><template v-if="key.tokenLimit"><span>{{ formatNumber(key.tokensUsed) }}</span><span class="usage-limit">/ {{ formatNumber(key.tokenLimit) }}</span></template><template v-else>{{ formatNumber(key.tokensUsed) }} / ∞</template></strong></div>
                <div class="kv col-span-2"><span>Requests</span><strong class="num">{{ quota(key.requestQuotaUsed, key.requestLimit) }}</strong><RequestCounts :succeeded="key.requestSuccessCount" :failed="key.requestFailureCount" :canceled="key.requestCanceledCount" :rejected="key.requestRejectedCount" /></div>
              </div>
              <div class="mt-3 flex flex-wrap gap-2">
                <AppButton small @click="openEdit(key)">Edit</AppButton>
                <AppButton small @click="run(async () => { secrets = [await service.rotate(key.id, scenario)]; createOpen = true }, 'Key rotated')">Rotate</AppButton>
                <AppButton small kind="danger" @click="confirming = key">Delete</AppButton>
              </div>
            </article>
            <p v-if="!visibleKeys.length" class="record text-center text-sm text-[var(--ink-soft)]">No matching keys.</p>
          </div>
          <Pager :note="pageRange(page, visibleKeys.length, keysTotal, service.pageSize)" :page="page" :has-more="keyHasMore" :disabled="loading" @prev="page -= 1" @next="page += 1" />
        </template>
      </section>

      <section id="requests" class="panel scroll-mt-20">
        <h2 class="section-title">Request explorer</h2>
        <p class="section-copy">{{ selectedId ? `Recent requests for ${selectedId}` : 'Recent requests across customer keys.' }}</p>
        <template v-if="snapshot">
          <div class="table-shell only-desktop mt-4">
            <div class="table-scroll">
              <table class="data-table">
                <caption class="sr-only">Recent requests</caption>
                <thead><tr><th>Time</th><th>Model</th><th>Transport</th><th>Status</th><th>Reason</th><th class="text-right">TTFT</th><th class="text-right">Latency</th><th class="text-right">Billed</th></tr></thead>
                <tbody>
                  <tr v-for="row in requestSlice" :key="row.requestId">
                    <td><time class="block" :datetime="row.requestedAt">{{ splitWhen(row.requestedAt).date }}</time><span class="sub">{{ splitWhen(row.requestedAt).time }}</span></td>
                    <td class="num max-w-48 truncate text-xs">{{ row.model }}</td>
                    <td>{{ row.transport }}</td>
                    <td><StatusChip :value="row.status" /></td>
                    <td class="text-xs">{{ rejectionLabel(row.rejectionCode) }}</td>
                    <td class="num text-right text-xs">{{ row.ttftMs ? `${formatNumber(row.ttftMs)} ms` : '—' }}</td>
                    <td class="num text-right text-xs">{{ row.latencyMs ? `${formatNumber(row.latencyMs)} ms` : '—' }}</td>
                    <td class="num text-right text-xs">{{ formatNumber(row.billed) }}</td>
                  </tr>
                  <tr v-if="!requestSlice.length"><td colspan="8" class="py-10 text-center text-[var(--ink-soft)]">No requests recorded yet.</td></tr>
                </tbody>
              </table>
            </div>
          </div>
          <div class="card-list only-mobile mt-4">
            <article v-for="row in requestSlice" :key="row.requestId" class="record">
              <div class="flex items-start justify-between gap-3"><strong class="num truncate text-xs">{{ row.model }}</strong><StatusChip :value="row.status" /></div>
              <div class="mt-3 grid grid-cols-2 gap-3 text-xs">
                <div class="kv"><span>Time</span><strong>{{ splitWhen(row.requestedAt).date }}</strong></div>
                <div class="kv"><span>Reason</span><strong>{{ rejectionLabel(row.rejectionCode) }}</strong></div>
                <div class="kv"><span>TTFT</span><strong class="num">{{ row.ttftMs ? `${formatNumber(row.ttftMs)} ms` : '—' }}</strong></div>
                <div class="kv"><span>Billed</span><strong class="num">{{ formatNumber(row.billed) }}</strong></div>
              </div>
            </article>
            <p v-if="!requestSlice.length" class="record text-center text-sm text-[var(--ink-soft)]">No requests recorded yet.</p>
          </div>
          <Pager :note="pageRange(requestPage, requestSlice.length, requestTotal, service.pageSize)" :page="requestPage" :has-more="requestHasMore" :disabled="loading" @prev="requestPage -= 1" @next="requestPage += 1" />
        </template>
      </section>
    </main>

    <AppModal wide :open="createOpen" title="Create keys" @close="createOpen = false">
      <p class="section-copy">Create one key or a batch of up to 500. The secret is shown once.</p>
      <form class="mt-4 grid gap-3" @submit.prevent="createKeys">
        <AppField label="Label" :hint="createDraft.count > 1 ? 'Batch labels get a short unique suffix.' : 'Saved exactly as typed.'">
          <div class="flex gap-2"><AppTextInput v-model="createDraft.label" :maxlength="200" placeholder="Any name" /><AppButton type="button" @click="createDraft.label = randomLabel()">Generate</AppButton></div>
        </AppField>
        <AppField label="Customer"><AppTextInput v-model="createDraft.customer" placeholder="Customer name" /></AppField>
        <div class="grid gap-3 sm:grid-cols-2">
          <AppField label="Quantity"><AppNumberInput v-model="createDraft.count" :min="1" :max="500" /></AppField>
          <AppField label="Token limit"><AppNumberInput v-model="createDraft.tokenLimit" :disabled="createDraft.unlimitedTokens" /></AppField>
        </div>
        <AppField label="Expires at"><AppDateTime v-model="createDraft.expiresLocal" :disabled="createDraft.neverExpires" /></AppField>
        <AppSwitch :on="createDraft.neverExpires" text="Never expires" label="Never expires" @toggle="createDraft.neverExpires = !createDraft.neverExpires" />
        <AppCheckbox :checked="createDraft.unlimitedTokens" label="Unlimited tokens" @toggle="createDraft.unlimitedTokens = !createDraft.unlimitedTokens" />
        <AppField label="Request limit" hint="Every recorded call counts, including failures, cancellations, and rejections.">
          <AppNumberInput v-model="createDraft.requestLimit" :disabled="createDraft.unlimitedRequests" />
        </AppField>
        <AppCheckbox :checked="createDraft.unlimitedRequests" label="Unlimited requests" @toggle="createDraft.unlimitedRequests = !createDraft.unlimitedRequests" />
        <AppField label="Internal note"><AppTextInput v-model="createDraft.note" placeholder="Optional note" /></AppField>
        <AppButton kind="primary" type="submit">Create {{ createDraft.count || 1 }} key{{ createDraft.count === 1 ? '' : 's' }}</AppButton>
      </form>
      <div v-if="secrets.length" class="mt-4 grid gap-3">
        <pre class="secret">{{ secrets.join('\n') }}</pre>
        <AppButton @click="copyText(secrets.join('\n')).then((ok) => notify(ok ? 'Secrets copied' : 'Unable to copy'))">Copy all secrets</AppButton>
      </div>
    </AppModal>

    <AppModal wide :open="editOpen" title="Selected key" @close="editOpen = false; selectedId = ''; requestPage = 1">
      <template v-if="editDraft">
        <div class="rounded-[10px] border border-[var(--line)] bg-[var(--bg-float)] p-3">
          <div class="field-label">Key ID</div>
          <div class="mt-1 flex items-start justify-between gap-3"><code class="num text-xs">{{ editDraft.id }}</code><AppButton small @click="copyText(editDraft.id).then((ok) => notify(ok ? 'Key ID copied' : 'Unable to copy'))">Copy</AppButton></div>
          <p class="field-hint mt-1">Permanent. This ID cannot be edited.</p>
        </div>
        <div class="mt-3 grid gap-3">
          <AppField label="Label"><AppTextInput v-model="editDraft.label" :maxlength="200" /></AppField>
          <AppField label="Customer"><AppTextInput v-model="editDraft.customer" /></AppField>
          <AppField label="Token limit"><AppNumberInput v-model="editDraft.tokenLimit" /></AppField>
          <AppField label="Request limit"><AppNumberInput v-model="editDraft.requestLimit" :disabled="editDraft.unlimitedRequests" /></AppField>
          <AppCheckbox :checked="editDraft.unlimitedRequests" label="Unlimited requests" @toggle="editDraft.unlimitedRequests = !editDraft.unlimitedRequests" />
          <AppField label="Expires at"><AppDateTime v-model="editDraft.expiresLocal" :disabled="editDraft.neverExpires" /></AppField>
          <AppSwitch :on="editDraft.neverExpires" text="Never expires" label="Never expires" @toggle="editDraft.neverExpires = !editDraft.neverExpires" />
          <div>
            <span class="field-label">Status</span>
            <AppSegmented class="mt-1.5" :model-value="editDraft.status" label="Key status" :options="[{ value: 'active', label: 'Enabled' }, { value: 'disabled', label: 'Disabled' }]" @update:model-value="editDraft.status = $event as 'active' | 'disabled'" />
          </div>
          <p class="field-hint">Reset clears billed tokens only. It does not restore request quota.</p>
          <div class="flex flex-wrap gap-2">
            <AppButton kind="primary" @click="saveEdit">Save</AppButton>
            <AppButton @click="run(() => service.resetUsage(editDraft!.id, scenario), 'Token usage reset')">Reset usage</AppButton>
          </div>
        </div>
      </template>
    </AppModal>

    <AppModal :open="Boolean(statusTarget)" kicker="Confirm" :title="statusTarget?.status === 'disabled' ? 'Enable this key?' : 'Disable this key?'" @close="statusTarget = null">
      <p class="section-copy">“{{ statusTarget?.label }}” will {{ statusTarget?.status === 'disabled' ? 'be allowed to call again' : 'stop accepting calls until you enable it' }}.</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton @click="statusTarget = null">Cancel</AppButton>
        <AppButton kind="primary" @click="run(() => service.setEnabled(statusTarget!.id, statusTarget!.status === 'disabled', scenario), statusTarget!.status === 'disabled' ? 'Key enabled' : 'Key disabled').then(() => statusTarget = null)">Confirm</AppButton>
      </div>
    </AppModal>
    <AppModal :open="Boolean(confirming)" kicker="Confirm" title="Delete this key?" @close="confirming = null">
      <p class="section-copy">“{{ confirming?.label }}” will stop working immediately. This action cannot be undone.</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton @click="confirming = null">Cancel</AppButton>
        <AppButton kind="danger" @click="run(() => service.remove(confirming!.id, scenario), 'Key deleted').then(() => confirming = null)">Delete key</AppButton>
      </div>
    </AppModal>
    <div v-if="toast" class="toast" role="status">{{ toast }}</div>
  </AdminShell>
</template>
