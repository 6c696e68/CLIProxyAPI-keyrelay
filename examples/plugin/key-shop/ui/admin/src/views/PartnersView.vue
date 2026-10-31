<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import FilterBar from '@/components/FilterBar.vue'
import PageHeader from '@/components/PageHeader.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppCheckbox from '@/components/ui/AppCheckbox.vue'
import AppField from '@/components/ui/AppField.vue'
import AppModal from '@/components/ui/AppModal.vue'
import AppSegmented from '@/components/ui/AppSegmented.vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import AppSheet from '@/components/ui/AppSheet.vue'
import AppSwitch from '@/components/ui/AppSwitch.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import { useLoadGuard } from '@/composables/useLoadGuard'
import { useSubmitGuard } from '@/composables/useSubmitGuard'
import { bindViewBusy, notifyKey, refreshKey } from '@/composables/useToast'
import { copyText, formatNumber, formatWhen } from '@/lib/format'
import type { AdminService, Partner, PartnerCall, PartnerInput, PartnerOrder, PartnerSettings, PartnerStats, Plan, Scenario } from '@/types'

const props = withDefaults(defineProps<{ service: AdminService; scenario?: Scenario }>(), { scenario: 'ready' })
const notify = inject(notifyKey, () => {})
const refreshTick = inject(refreshKey, ref(0))
const partners = ref<Partner[]>([])
const plans = ref<Plan[]>([])
const error = ref('')
const { loading, load: runLoad } = useLoadGuard()

const editorOpen = ref(false)
const editing = ref<Partner | null>(null)
const name = ref('')
const status = ref<'active' | 'disabled'>('active')
const allowedPlanIds = ref<string[]>([])
const useDefaultDailyCap = ref(true)
const dailyCapText = ref('')
const useDefaultMaxQuantity = ref(true)
const maxQuantityText = ref('')
const { pending: saving, guard: guardSave } = useSubmitGuard()
const { pending: acting, guard: guardAct } = useSubmitGuard()

const secretOpen = ref(false)
const secret = ref('')
const secretFor = ref('')
const copying = ref(false)
const rotateTarget = ref<Partner | null>(null)
const pendingDelete = ref<Partner | null>(null)
const deleteError = ref('')

const detailPartner = ref<Partner | null>(null)
const calls = ref<PartnerCall[]>([])
const orders = ref<PartnerOrder[]>([])
const callOp = ref('')
const callStatus = ref('')
const orderState = ref('')
const orderRef = ref('')
const callCursor = ref('')
const orderCursor = ref('')
const callsLoading = ref(false)
const ordersLoading = ref(false)
const callsMore = ref(false)
const ordersMore = ref(false)

const settingsLoading = ref(false)
const settingsLoaded = ref(false)
const settingsError = ref('')
const { pending: settingsSaving, guard: guardSettings } = useSubmitGuard()
const apiEnabled = ref(false)
const publicBaseUrl = ref('')
const publicDashboardPath = ref('')
const maxQuantity = ref('')
const dailyKeyCap = ref('')
const maxBodyKb = ref('')
const alertMin = ref('')
const logRetentionDays = ref('')
const labelTemplate = ref('')
const noteTemplate = ref('')
bindViewBusy(() => loading.value || saving.value || acting.value || settingsSaving.value)

const statusOptions = [{ value: 'active', label: 'Active' }, { value: 'disabled', label: 'Disabled' }]
const callOpOptions = [
  { value: '', label: 'All operations' },
  { value: 'health', label: 'health' },
  { value: 'plans', label: 'plans' },
  { value: 'mint', label: 'mint' },
  { value: 'confirm', label: 'confirm' },
  { value: 'void', label: 'void' },
  { value: 'status', label: 'status' },
  { value: 'refs', label: 'refs' },
]
const callStatusOptions = [
  { value: '', label: 'All results' },
  { value: 'ok', label: 'OK' },
  { value: 'error', label: 'Error' },
]
const orderStateOptions = [
  { value: '', label: 'All states' },
  { value: 'minted', label: 'Minted' },
  { value: 'confirmed', label: 'Confirmed' },
  { value: 'voided', label: 'Voided' },
]

const detailStats = computed(() => (detailPartner.value ? statsOf(detailPartner.value) : null))
const detailAllowed = computed(() => (detailPartner.value ? allowedLabel(detailPartner.value) : ''))
const detailCaps = computed(() => (detailPartner.value ? capsLabel(detailPartner.value) : ''))

function emptyStats(): PartnerStats {
  return {
    callsOk: 0, callsClientError: 0, callsServerError: 0,
    minted: 0, confirmed: 0, voided: 0, unconfirmedOverdue: 0,
    dailyUsed: 0, dailyCap: 0,
  }
}
function statsOf(partner: Partner): PartnerStats {
  return partner.stats || emptyStats()
}
function planName(id: string): string {
  return plans.value.find((plan) => plan.id === id)?.name || id
}
function allowedLabel(partner: Partner): string {
  return partner.allowedPlanIds.length ? partner.allowedPlanIds.map(planName).join(', ') : 'All plans'
}
function capsLabel(partner: Partner): string {
  const daily = partner.dailyKeyCap == null ? 'default' : formatNumber(partner.dailyKeyCap)
  const max = partner.maxQuantity == null ? 'default' : String(partner.maxQuantity)
  return `Daily ${daily} · Max ${max}`
}
function callsLabel(stats: PartnerStats): string {
  const failed = stats.callsClientError + stats.callsServerError
  return `${formatNumber(stats.callsOk)} ok · ${formatNumber(failed)} err`
}
function ordersLabel(stats: PartnerStats): string {
  return `${stats.minted} / ${stats.confirmed} / ${stats.voided}`
}
function dailyLabel(stats: PartnerStats): string {
  return `${formatNumber(stats.dailyUsed)} / ${stats.dailyCap > 0 ? formatNumber(stats.dailyCap) : '∞'}`
}
function callTone(call: PartnerCall): string {
  if (call.httpStatus >= 500) return 'chip-bad'
  if (call.httpStatus >= 400) return 'chip-warn'
  return 'chip-good'
}
function orderTone(state: string): string {
  if (state === 'confirmed') return 'chip-good'
  if (state === 'voided') return 'chip-bad'
  return 'chip-warn'
}
function parseOptionalNumber(text: string): number | null | undefined {
  const trimmed = text.trim()
  if (!trimmed) return null
  if (!/^\d+$/.test(trimmed)) return undefined
  return Number(trimmed)
}

function load() {
  return runLoad(async () => {
    if (props.scenario === 'loading') {
      partners.value = []
      return
    }
    error.value = ''
    try {
      const [nextPartners, nextPlans] = await Promise.all([
        props.service.partners(props.scenario),
        props.service.plans(props.scenario).catch(() => [] as Plan[]),
      ])
      partners.value = nextPartners
      plans.value = nextPlans
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Request failed'
    }
  }).then(() => loadSettings())
}

// loadSettings gates the settings card: the form stays read-only until a GET
// succeeds, so a blank form can never overwrite the stored partner config.
async function loadSettings() {
  if (settingsLoading.value) return
  settingsLoading.value = true
  settingsLoaded.value = false
  settingsError.value = ''
  try {
    applySettings(await props.service.partnerSettings(props.scenario))
    settingsLoaded.value = true
  } catch (err) {
    settingsError.value = err instanceof Error ? err.message : 'Partner settings could not be loaded.'
  } finally {
    settingsLoading.value = false
  }
}

function applySettings(next: PartnerSettings) {
  settingsError.value = ''
  apiEnabled.value = next.apiEnabled
  publicBaseUrl.value = next.publicBaseUrl
  publicDashboardPath.value = next.publicDashboardPath
  maxQuantity.value = next.maxQuantity == null ? '' : String(next.maxQuantity)
  dailyKeyCap.value = next.dailyKeyCap == null ? '' : String(next.dailyKeyCap)
  maxBodyKb.value = next.maxBodyKb == null ? '' : String(next.maxBodyKb)
  alertMin.value = next.alertMin == null ? '' : String(next.alertMin)
  logRetentionDays.value = next.logRetentionDays == null ? '' : String(next.logRetentionDays)
  labelTemplate.value = next.labelTemplate
  noteTemplate.value = next.noteTemplate
}

async function saveSettings() {
  if (settingsSaving.value || settingsLoading.value || !settingsLoaded.value || settingsError.value) return
  const parsed = {
    maxQuantity: parseOptionalNumber(maxQuantity.value),
    dailyKeyCap: parseOptionalNumber(dailyKeyCap.value),
    maxBodyKb: parseOptionalNumber(maxBodyKb.value),
    alertMin: parseOptionalNumber(alertMin.value),
    logRetentionDays: parseOptionalNumber(logRetentionDays.value),
  }
  if (Object.values(parsed).some((value) => value === undefined)) {
    notify('Settings numbers must be whole numbers or empty.')
    return
  }
  await guardSettings(async () => {
    try {
      await props.service.savePartnerSettings({
        apiEnabled: apiEnabled.value,
        publicBaseUrl: publicBaseUrl.value,
        publicDashboardPath: publicDashboardPath.value,
        maxQuantity: parsed.maxQuantity ?? null,
        dailyKeyCap: parsed.dailyKeyCap ?? null,
        maxBodyKb: parsed.maxBodyKb ?? null,
        alertMin: parsed.alertMin ?? null,
        logRetentionDays: parsed.logRetentionDays ?? null,
        labelTemplate: labelTemplate.value,
        noteTemplate: noteTemplate.value,
      }, props.scenario)
      notify('Partner API settings saved')
      const refreshed = await props.service.partnerSettings(props.scenario).catch(() => null)
      if (refreshed) applySettings(refreshed)
    } catch (err) {
      notify(err instanceof Error ? err.message : 'Settings could not be saved')
    }
  })
}

function resetEditor() {
  name.value = ''
  status.value = 'active'
  allowedPlanIds.value = []
  useDefaultDailyCap.value = true
  dailyCapText.value = ''
  useDefaultMaxQuantity.value = true
  maxQuantityText.value = ''
}
function openCreate() {
  if (saving.value || acting.value) return
  editing.value = null
  resetEditor()
  editorOpen.value = true
}
function openEdit(partner: Partner) {
  if (saving.value || acting.value) return
  editing.value = partner
  name.value = partner.name
  status.value = partner.status
  allowedPlanIds.value = [...partner.allowedPlanIds]
  useDefaultDailyCap.value = partner.dailyKeyCap == null
  dailyCapText.value = partner.dailyKeyCap == null ? '' : String(partner.dailyKeyCap)
  useDefaultMaxQuantity.value = partner.maxQuantity == null
  maxQuantityText.value = partner.maxQuantity == null ? '' : String(partner.maxQuantity)
  editorOpen.value = true
}
function togglePlan(id: string) {
  const index = allowedPlanIds.value.indexOf(id)
  if (index >= 0) allowedPlanIds.value.splice(index, 1)
  else allowedPlanIds.value.push(id)
}
async function savePartner() {
  if (saving.value || acting.value) return
  if (!name.value.trim()) return
  const dailyCap = parseOptionalNumber(dailyCapText.value)
  const maxQuantity = parseOptionalNumber(maxQuantityText.value)
  if (dailyCap === undefined || maxQuantity === undefined) {
    notify('Caps must be whole numbers or empty.')
    return
  }
  await guardSave(async () => {
    try {
      const input: PartnerInput = {
        id: editing.value?.id,
        name: name.value,
        status: status.value,
        allowedPlanIds: [...allowedPlanIds.value],
        dailyKeyCap: useDefaultDailyCap.value ? null : dailyCap,
        maxQuantity: useDefaultMaxQuantity.value ? null : maxQuantity,
      }
      if (editing.value) {
        await props.service.updatePartner(input, props.scenario)
        notify('Partner saved')
      } else {
        const created = await props.service.createPartner(input, props.scenario)
        notify('Partner created')
        showSecret(created.secret, created.partner.name)
      }
      editorOpen.value = false
      await load()
    } catch (err) {
      notify(err instanceof Error ? err.message : 'Partner could not be saved')
    }
  })
}

function showSecret(value: string, partnerName: string) {
  secret.value = value
  secretFor.value = partnerName
  secretOpen.value = true
}
function closeSecret() {
  secretOpen.value = false
  secret.value = ''
  secretFor.value = ''
}
async function copySecret() {
  if (!secret.value || copying.value) return
  copying.value = true
  try {
    notify((await copyText(secret.value)) ? 'Secret copied' : 'Unable to copy the secret')
  } finally {
    copying.value = false
  }
}

async function rotate() {
  const target = rotateTarget.value
  if (!target || acting.value) return
  await guardAct(async () => {
    try {
      const result = await props.service.rotatePartner(target.id, props.scenario)
      rotateTarget.value = null
      showSecret(result.secret, result.partner.name)
      notify('Secret rotated')
      await load()
    } catch (err) {
      notify(err instanceof Error ? err.message : 'Secret could not be rotated')
    }
  })
}
async function removePartner() {
  const target = pendingDelete.value
  if (!target || acting.value) return
  deleteError.value = ''
  await guardAct(async () => {
    try {
      await props.service.removePartner(target.id, props.scenario)
      pendingDelete.value = null
      notify('Partner deleted')
      await load()
    } catch (err) {
      deleteError.value = err instanceof Error ? err.message : 'Partner could not be deleted'
    }
  })
}

function openDetail(partner: Partner) {
  detailPartner.value = partner
  callOp.value = ''
  callStatus.value = ''
  orderState.value = ''
  orderRef.value = ''
  callCursor.value = ''
  orderCursor.value = ''
  calls.value = []
  orders.value = []
  void loadCalls(true)
  void loadOrders(true)
}
function closeDetail() {
  detailPartner.value = null
  calls.value = []
  orders.value = []
}
async function loadCalls(reset: boolean) {
  const partner = detailPartner.value
  if (!partner) return
  if (reset) callsLoading.value = true
  else callsMore.value = true
  try {
    const page = await props.service.partnerCalls({
      partner: partner.id, op: callOp.value, status: callStatus.value,
      from: '', to: '', limit: 20, cursor: reset ? '' : callCursor.value,
    }, props.scenario)
    calls.value = reset ? page.calls : [...calls.value, ...page.calls]
    callCursor.value = page.nextCursor
  } catch (err) {
    notify(err instanceof Error ? err.message : 'Call log failed')
  } finally {
    callsLoading.value = false
    callsMore.value = false
  }
}
async function loadOrders(reset: boolean) {
  const partner = detailPartner.value
  if (!partner) return
  if (reset) ordersLoading.value = true
  else ordersMore.value = true
  try {
    const page = await props.service.partnerOrders({
      partner: partner.id, state: orderState.value, ref: orderRef.value.trim(),
      from: '', to: '', limit: 20, cursor: reset ? '' : orderCursor.value,
    }, props.scenario)
    orders.value = reset ? page.orders : [...orders.value, ...page.orders]
    orderCursor.value = page.nextCursor
  } catch (err) {
    notify(err instanceof Error ? err.message : 'Orders failed')
  } finally {
    ordersLoading.value = false
    ordersMore.value = false
  }
}

watch([callOp, callStatus], () => {
  if (detailPartner.value) void loadCalls(true)
})
watch(orderState, () => {
  if (detailPartner.value) void loadOrders(true)
})
watch(() => props.scenario, () => { void load() }, { immediate: true })
watch(refreshTick, () => { void load() })
</script>

<template>
  <div>
    <PageHeader kicker="Sales" title="Partners" copy="Partners mint keys through the public partner API. The secret is shown once, on create or rotate.">
      <AppButton kind="primary" :disabled="saving || acting" @click="openCreate">{{ saving ? 'Working...' : 'New partner' }}</AppButton>
    </PageHeader>
    <p v-if="error" class="alert mb-4" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" :disabled="loading" @click="load">{{ loading ? 'Loading...' : 'Retry' }}</button></p>
    <section class="panel">
      <p v-if="loading && !partners.length" role="status">Loading partners…</p>
      <template v-else>
        <div class="table-shell only-desktop">
          <div class="table-scroll">
            <table class="data-table">
              <caption class="sr-only">Sales partners</caption>
              <thead><tr><th>Partner</th><th>Status</th><th>24h calls</th><th>24h orders</th><th>Overdue</th><th>Daily keys</th><th class="actions">Actions</th></tr></thead>
              <tbody>
                <tr v-for="partner in partners" :key="partner.id">
                  <td>
                    <strong>{{ partner.name }}</strong>
                    <div class="sub num">{{ partner.secretPrefix }}…{{ partner.secretLast4 }}</div>
                    <div class="sub">{{ allowedLabel(partner) }} · {{ capsLabel(partner) }}</div>
                  </td>
                  <td><span class="chip" :class="partner.status === 'active' ? 'chip-good' : 'chip-bad'"><i />{{ partner.status === 'active' ? 'Active' : 'Disabled' }}</span></td>
                  <td class="text-xs">{{ callsLabel(statsOf(partner)) }}</td>
                  <td class="text-xs">{{ ordersLabel(statsOf(partner)) }}<div class="sub">minted / confirmed / voided</div></td>
                  <td><span class="chip" :class="statsOf(partner).unconfirmedOverdue > 0 ? 'chip-bad' : ''"><i />{{ statsOf(partner).unconfirmedOverdue }}</span></td>
                  <td class="num text-xs">{{ dailyLabel(statsOf(partner)) }}</td>
                  <td class="actions">
                    <div class="flex gap-1">
                      <AppButton small @click="openDetail(partner)">Detail</AppButton>
                      <AppButton small :disabled="saving || acting" @click="openEdit(partner)">Edit</AppButton>
                      <AppButton small :disabled="saving || acting" @click="(saving || acting) ? undefined : (rotateTarget = partner)">Rotate</AppButton>
                      <AppButton small kind="danger" :disabled="saving || acting" @click="(saving || acting) ? undefined : (pendingDelete = partner, deleteError = '')">Delete</AppButton>
                    </div>
                  </td>
                </tr>
                <tr v-if="!partners.length"><td colspan="7" class="py-10 text-center text-[var(--ink-soft)]">No partners yet.</td></tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="card-list only-mobile">
          <article v-for="partner in partners" :key="partner.id" class="record">
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <strong>{{ partner.name }}</strong>
                <p class="sub num">{{ partner.secretPrefix }}…{{ partner.secretLast4 }}</p>
              </div>
              <span class="chip" :class="partner.status === 'active' ? 'chip-good' : 'chip-bad'"><i />{{ partner.status === 'active' ? 'Active' : 'Disabled' }}</span>
            </div>
            <p class="sub mt-2">{{ allowedLabel(partner) }} · {{ capsLabel(partner) }}</p>
            <div class="mt-3 grid grid-cols-2 gap-3">
              <div class="kv"><span>24h calls</span><strong>{{ callsLabel(statsOf(partner)) }}</strong></div>
              <div class="kv"><span>24h orders</span><strong>{{ ordersLabel(statsOf(partner)) }}</strong></div>
              <div class="kv"><span>Overdue</span><strong :class="{ 'text-[var(--bad)]': statsOf(partner).unconfirmedOverdue > 0 }">{{ statsOf(partner).unconfirmedOverdue }}</strong></div>
              <div class="kv"><span>Daily keys</span><strong class="num">{{ dailyLabel(statsOf(partner)) }}</strong></div>
            </div>
            <div class="mt-3 flex flex-wrap gap-2">
              <AppButton small @click="openDetail(partner)">Detail</AppButton>
              <AppButton small :disabled="saving || acting" @click="openEdit(partner)">Edit</AppButton>
              <AppButton small :disabled="saving || acting" @click="(saving || acting) ? undefined : (rotateTarget = partner)">Rotate</AppButton>
              <AppButton small kind="danger" :disabled="saving || acting" @click="(saving || acting) ? undefined : (pendingDelete = partner, deleteError = '')">Delete</AppButton>
            </div>
          </article>
          <p v-if="!partners.length" class="record text-center text-sm text-[var(--ink-soft)]">No partners yet.</p>
        </div>
      </template>
    </section>

    <section class="panel mt-5">
      <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0">
          <p class="page-kicker">Configuration</p>
          <h2 class="section-title">Partner API settings</h2>
          <p class="page-copy">These values apply to every partner. Leave a number empty to keep the server default; saving keeps every other plugin config key unchanged.</p>
        </div>
        <AppButton kind="primary" :disabled="!settingsLoaded || settingsLoading || settingsSaving || Boolean(settingsError)" @click="saveSettings">{{ settingsSaving ? 'Saving...' : 'Save settings' }}</AppButton>
      </div>
      <div v-if="settingsError" class="alert mb-3 flex flex-wrap items-center gap-2" role="alert">
        <span>{{ settingsError }}</span>
        <AppButton small :disabled="settingsLoading" @click="loadSettings">{{ settingsLoading ? 'Loading...' : 'Retry' }}</AppButton>
      </div>
      <p v-if="settingsLoading" role="status">Loading settings…</p>
      <p v-else-if="!settingsLoaded" class="sub">Settings are unavailable. Retry above before saving.</p>
      <div v-else class="grid gap-3 sm:grid-cols-2">
        <div class="sm:col-span-2">
          <AppSwitch :on="apiEnabled" text="Partner API enabled" label="Partner API enabled" @toggle="apiEnabled = !apiEnabled" />
        </div>
        <AppField label="Public base URL" hint="Empty uses the request Host. Absolute http(s) URL without a trailing slash.">
          <AppTextInput v-model="publicBaseUrl" placeholder="https://shop.example.com" />
        </AppField>
        <AppField label="Public dashboard path" hint="Empty uses /client.">
          <AppTextInput v-model="publicDashboardPath" placeholder="/client" />
        </AppField>
        <AppField label="Max quantity per mint" hint="Empty uses the server default.">
          <AppTextInput v-model="maxQuantity" placeholder="Server default" />
        </AppField>
        <AppField label="Daily key cap" hint="0 means unlimited. Empty uses the server default.">
          <AppTextInput v-model="dailyKeyCap" placeholder="Server default" />
        </AppField>
        <AppField label="Max body KB" hint="Empty uses the server default.">
          <AppTextInput v-model="maxBodyKb" placeholder="Server default" />
        </AppField>
        <AppField label="Unconfirmed alert minutes" hint="Empty uses the server default.">
          <AppTextInput v-model="alertMin" placeholder="Server default" />
        </AppField>
        <AppField label="Partner log retention days" hint="Empty uses the server default.">
          <AppTextInput v-model="logRetentionDays" placeholder="Server default" />
        </AppField>
        <AppField label="Key label template" hint="Placeholders: {plan}, {partner}, {ref}, {i}.">
          <AppTextInput v-model="labelTemplate" placeholder="{plan}-{ref}-{i}" />
        </AppField>
        <AppField label="Key note template" hint="Placeholders: {plan}, {partner}, {ref}, {i}.">
          <AppTextInput v-model="noteTemplate" placeholder="partner:{partner} ref:{ref}" />
        </AppField>
      </div>
    </section>

    <AppSheet :open="editorOpen" :title="editing ? 'Edit partner' : 'New partner'" :locked="saving || acting" @close="editorOpen = false">
      <form class="grid gap-3" @submit.prevent="savePartner">
        <AppField label="Name" hint="Required and unique among live partners.">
          <AppTextInput v-model="name" :maxlength="80" placeholder="Telegram shop" />
        </AppField>
        <div v-if="editing">
          <span class="field-label">Status</span>
          <AppSegmented class="mt-1.5" :model-value="status" label="Partner status" :options="statusOptions" @update:model-value="status = $event as 'active' | 'disabled'" />
        </div>
        <div class="grid gap-2">
          <span class="field-label">Allowed plans</span>
          <p class="field-hint">No selection means every plan.</p>
          <AppCheckbox v-for="plan in plans" :key="plan.id" :checked="allowedPlanIds.includes(plan.id)" :label="plan.name" @toggle="togglePlan(plan.id)" />
          <p v-if="!plans.length" class="sub">No plans yet.</p>
        </div>
        <div class="grid gap-2">
          <span class="field-label">Daily key cap</span>
          <AppSwitch :on="useDefaultDailyCap" text="Use server default" label="Use default daily key cap" @toggle="useDefaultDailyCap = !useDefaultDailyCap" />
          <input v-model="dailyCapText" class="control num" inputmode="numeric" :disabled="useDefaultDailyCap" placeholder="0 = unlimited" />
          <span class="field-hint">0 means unlimited. Overrides the server default for this partner.</span>
        </div>
        <div class="grid gap-2">
          <span class="field-label">Max quantity per mint</span>
          <AppSwitch :on="useDefaultMaxQuantity" text="Use server default" label="Use default max quantity" @toggle="useDefaultMaxQuantity = !useDefaultMaxQuantity" />
          <input v-model="maxQuantityText" class="control num" inputmode="numeric" :disabled="useDefaultMaxQuantity" placeholder="Server default" />
          <span class="field-hint">Overrides the server default for this partner.</span>
        </div>
        <AppButton kind="primary" type="submit" :disabled="saving || acting || !name.trim()">{{ saving ? 'Saving...' : (editing ? 'Save partner' : 'Create partner') }}</AppButton>
      </form>
    </AppSheet>

    <AppModal :open="secretOpen" title="Copy the partner secret" @close="closeSecret">
      <p class="section-copy">This secret is shown once. Closing this dialog clears it from the page.</p>
      <p class="sub mt-2">{{ secretFor }}</p>
      <pre class="secret mt-3">{{ secret }}</pre>
      <AppButton class="mt-3" :disabled="copying" @click="copySecret">{{ copying ? 'Copying...' : 'Copy secret' }}</AppButton>
    </AppModal>

    <AppModal :open="Boolean(rotateTarget)" kicker="Confirm" title="Rotate this secret?" :locked="acting" @close="rotateTarget = null">
      <p class="section-copy">“{{ rotateTarget?.name }}” gets a new secret. The old secret stops working immediately, and the new one is shown once.</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton :disabled="acting" @click="rotateTarget = null">Cancel</AppButton>
        <AppButton kind="primary" :disabled="acting" @click="rotate">{{ acting ? 'Rotating...' : 'Rotate secret' }}</AppButton>
      </div>
    </AppModal>

    <AppModal :open="Boolean(pendingDelete)" kicker="Confirm" title="Delete this partner?" :locked="acting" @close="pendingDelete = null">
      <p class="section-copy">“{{ pendingDelete?.name }}” is soft-deleted. Its secret stops authenticating, but its orders and keys stay.</p>
      <p v-if="deleteError" class="alert mt-3" role="alert">{{ deleteError }}</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton :disabled="acting" @click="pendingDelete = null">Cancel</AppButton>
        <AppButton kind="danger" :disabled="acting" @click="removePartner">{{ acting ? 'Deleting...' : 'Delete partner' }}</AppButton>
      </div>
    </AppModal>

    <DetailDrawer :open="Boolean(detailPartner)" :title="detailPartner?.name || 'Partner'" kicker="Partner detail" @close="closeDetail">
      <template v-if="detailPartner && detailStats">
        <div class="kv"><span>Status</span><strong>{{ detailPartner.status === 'active' ? 'Active' : 'Disabled' }}</strong></div>
        <div class="kv"><span>Allowed plans</span><strong>{{ detailAllowed }}</strong></div>
        <div class="kv"><span>Caps</span><strong>{{ detailCaps }}</strong></div>
        <div class="kv"><span>Last used</span><strong>{{ formatWhen(detailPartner.lastUsedAt) }}</strong></div>
        <div class="kv"><span>24h calls</span><strong>{{ callsLabel(detailStats) }}</strong></div>
        <div class="kv"><span>24h orders</span><strong>{{ ordersLabel(detailStats) }}</strong></div>
        <div class="kv"><span>Unconfirmed overdue</span><strong :class="{ 'text-[var(--bad)]': detailStats.unconfirmedOverdue > 0 }">{{ detailStats.unconfirmedOverdue }}</strong></div>
        <div class="kv"><span>Daily keys</span><strong class="num">{{ dailyLabel(detailStats) }}</strong></div>
      </template>
      <template #extra>
        <section class="panel">
          <div class="mb-3 flex items-center justify-between gap-2">
            <h3 class="section-title">Call log</h3>
            <AppButton small :disabled="callsLoading" @click="loadCalls(true)">{{ callsLoading ? 'Loading...' : 'Refresh' }}</AppButton>
          </div>
          <FilterBar>
            <AppSelect v-model="callOp" :options="callOpOptions" label="Filter calls by operation" />
            <AppSelect v-model="callStatus" :options="callStatusOptions" label="Filter calls by result" />
          </FilterBar>
          <p v-if="callsLoading && !calls.length" role="status">Loading calls…</p>
          <template v-else>
            <div class="table-shell">
              <div class="table-scroll">
                <table class="data-table fit">
                  <caption class="sr-only">Partner call log</caption>
                  <thead><tr><th>When</th><th>Op</th><th>Status</th><th>Code</th><th>Ref</th><th class="text-right">Latency</th><th>Client IP</th></tr></thead>
                  <tbody>
                    <tr v-for="row in calls" :key="row.id">
                      <td class="text-xs">{{ formatWhen(row.at) }}</td>
                      <td class="text-xs">{{ row.op }}</td>
                      <td><span class="chip" :class="callTone(row)"><i />{{ row.httpStatus }}</span></td>
                      <td class="text-xs">{{ row.code || '—' }}</td>
                      <td class="num text-xs">{{ row.ref || '—' }}</td>
                      <td class="num text-right text-xs">{{ formatNumber(row.latencyMs) }} ms</td>
                      <td class="num text-xs">{{ row.clientIp || '—' }}</td>
                    </tr>
                    <tr v-if="!calls.length"><td colspan="7" class="py-6 text-center text-[var(--ink-soft)]">No calls for this filter.</td></tr>
                  </tbody>
                </table>
              </div>
            </div>
            <AppButton v-if="callCursor" small class="mt-3" :disabled="callsMore" @click="loadCalls(false)">{{ callsMore ? 'Loading...' : 'Load more calls' }}</AppButton>
          </template>
        </section>

        <section class="panel mt-4">
          <div class="mb-3 flex items-center justify-between gap-2">
            <h3 class="section-title">Orders</h3>
            <AppButton small :disabled="ordersLoading" @click="loadOrders(true)">{{ ordersLoading ? 'Loading...' : 'Refresh' }}</AppButton>
          </div>
          <FilterBar>
            <AppSelect v-model="orderState" :options="orderStateOptions" label="Filter orders by state" />
            <div class="flex gap-2">
              <AppTextInput v-model="orderRef" placeholder="Filter by ref" />
              <AppButton small :disabled="ordersLoading" @click="loadOrders(true)">Search</AppButton>
            </div>
          </FilterBar>
          <p v-if="ordersLoading && !orders.length" role="status">Loading orders…</p>
          <template v-else>
            <div class="table-shell">
              <div class="table-scroll">
                <table class="data-table fit">
                  <caption class="sr-only">Partner orders</caption>
                  <thead><tr><th>Ref</th><th>State</th><th class="text-right">Qty</th><th>Order</th><th>Customer</th><th>Created</th></tr></thead>
                  <tbody>
                    <tr v-for="row in orders" :key="row.ref">
                      <td class="num text-xs">{{ row.ref }}</td>
                      <td><span class="chip" :class="orderTone(row.state)"><i />{{ row.state }}</span></td>
                      <td class="num text-right text-xs">{{ row.quantity }}</td>
                      <td class="num text-xs">{{ row.orderId || '—' }}</td>
                      <td class="text-xs">{{ row.customer || '—' }}</td>
                      <td class="text-xs">{{ formatWhen(row.createdAt) }}</td>
                    </tr>
                    <tr v-if="!orders.length"><td colspan="6" class="py-6 text-center text-[var(--ink-soft)]">No orders for this filter.</td></tr>
                  </tbody>
                </table>
              </div>
            </div>
            <AppButton v-if="orderCursor" small class="mt-3" :disabled="ordersMore" @click="loadOrders(false)">{{ ordersMore ? 'Loading...' : 'Load more orders' }}</AppButton>
          </template>
        </section>
      </template>
    </DetailDrawer>
  </div>
</template>
