<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import BulkBar from '@/components/BulkBar.vue'
import FilterBar from '@/components/FilterBar.vue'
import GroupSelect from '@/components/GroupSelect.vue'
import PageHeader from '@/components/PageHeader.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppCheckbox from '@/components/ui/AppCheckbox.vue'
import AppDateTime from '@/components/ui/AppDateTime.vue'
import AppField from '@/components/ui/AppField.vue'
import AppModal from '@/components/ui/AppModal.vue'
import AppNumberInput from '@/components/ui/AppNumberInput.vue'
import AppSegmented from '@/components/ui/AppSegmented.vue'
import AppSelect from '@/components/ui/AppSelect.vue'
import AppSheet from '@/components/ui/AppSheet.vue'
import AppSwitch from '@/components/ui/AppSwitch.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import Pager from '@/components/ui/Pager.vue'
import RequestCounts from '@/components/ui/RequestCounts.vue'
import StatusChip from '@/components/ui/StatusChip.vue'
import { useKeysQuery } from '@/composables/useKeysQuery'
import { useSelection } from '@/composables/useSelection'
import { useLoadGuard } from '@/composables/useLoadGuard'
import { useSubmitGuard } from '@/composables/useSubmitGuard'
import { bindViewBusy, notifyKey, refreshKey } from '@/composables/useToast'
import { toLocalInput, ttlSecondsFromLocal } from '@/lib/datetime'
import { defaultCreateDraft, rememberGroup, rememberedGroup } from '@/lib/drafts'
import { copyText, formatNumber, pageRange, quota, randomLabel, splitWhen } from '@/lib/format'
import type { AdminService, CreateDraft, CreatedTunnel, EditDraft, KeyRecord, ModelGroup, Partner, Scenario } from '@/types'

const props = withDefaults(defineProps<{ service: AdminService; scenario?: Scenario }>(), { scenario: 'ready' })
const notify = inject(notifyKey, () => {})
const refreshTick = inject(refreshKey, ref(0))
const groups = ref<ModelGroup[]>([])
const partners = ref<Partner[]>([])
const partner = ref('')
const keys = ref<KeyRecord[]>([])
const total = ref(0)
const error = ref('')
const { loading, load: runLoad } = useLoadGuard()
const { pending: acting, guard } = useSubmitGuard()
bindViewBusy(() => loading.value || acting.value)
const copying = ref(false)
const { query, status, group, page } = useKeysQuery(() => { void load() })
const selection = useSelection()
const bulkGroup = ref('')
const createOpen = ref(false)
const editOpen = ref(false)
const confirming = ref<KeyRecord | null>(null)
const statusTarget = ref<KeyRecord | null>(null)
const secrets = ref<string[]>([])
const createdTunnel = ref<CreatedTunnel | null>(null)
const secretOpen = ref(false)
const createDraft = ref<CreateDraft>(defaultCreateDraft())
const createRequestId = ref('')
const editDraft = ref<EditDraft | null>(null)
const expiryOpen = ref(false)
const limitsOpen = ref(false)
const deleteOpen = ref(false)
const expiryNever = ref(false)
const expiryLocal = ref(toLocalInput(new Date(Date.now() + 7 * 86_400_000)))
const limitTokens = ref(true)
const limitRequests = ref(false)
const tokenLimit = ref(100_000_000)
const unlimitedTokens = ref(false)
const requestLimit = ref(0)
const unlimitedRequests = ref(true)

const pageIds = computed(() => keys.value.map((key) => key.id))
const hasMore = computed(() => page.value * props.service.pageSize < total.value)
const statusOptions = [
  { value: '', label: 'All statuses' },
  { value: 'active', label: 'Active' },
  { value: 'disabled', label: 'Disabled' },
  { value: 'expired', label: 'Expired' },
  { value: 'exhausted', label: 'Exhausted' },
]
const partnerOptions = computed(() => [
  { value: '', label: 'All partners' },
  ...partners.value.map((item) => ({ value: item.id, label: item.name })),
])
function partnerLabel(id: string | undefined) {
  if (!id) return '—'
  return partners.value.find((item) => item.id === id)?.name || id
}
function groupLabel(id: string) {
  if (!id) return 'No group'
  return groups.value.find((item) => item.id === id)?.name || id
}
function reasons(key: KeyRecord) {
  return [key.requestLimitReached ? 'Request limit' : '', key.tokenLimitReached ? 'Token limit' : ''].filter(Boolean).join(' · ')
}

function load() {
  return runLoad(async () => {
  if (props.scenario === 'loading') {
    keys.value = []
    return
  }
  error.value = ''
  try {
    const [list, nextGroups, nextPartners] = await Promise.all([
      props.service.keys({ q: query.value, status: status.value, group: group.value, partner: partner.value, page: page.value, pageSize: props.service.pageSize }, props.scenario),
      props.service.groups(props.scenario),
      props.service.partners(props.scenario).catch(() => [] as Partner[]),
    ])
    keys.value = list.keys
    total.value = list.total
    groups.value = nextGroups
    partners.value = nextPartners
    if (!bulkGroup.value) bulkGroup.value = rememberedGroup(nextGroups.map((item) => item.id))
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Request failed'
  }
  })
}
async function run(action: () => Promise<void>, message: string) {
  let ok = false
  await guard(async () => {
    try {
      await action()
      await load()
      notify(message)
      ok = true
    } catch (err) {
      notify(err instanceof Error ? err.message : 'Request failed')
    }
  })
  return ok
}
function openCreate() {
  if (acting.value) return
  createDraft.value = defaultCreateDraft(rememberedGroup(groups.value.map((item) => item.id)))
  createRequestId.value = crypto.randomUUID()
  createOpen.value = true
}
function openEdit(key: KeyRecord) {
  if (acting.value) return
  editDraft.value = {
    id: key.id,
    label: key.label,
    customer: key.customer,
    note: key.note,
    groupId: key.groupId || rememberedGroup(groups.value.map((item) => item.id)),
    status: key.status === 'disabled' ? 'disabled' : 'active',
    expiresLocal: key.expiresAt ? toLocalInput(new Date(key.expiresAt)) : toLocalInput(new Date(Date.now() + 7 * 86_400_000)),
    neverExpires: !key.expiresAt,
    tokenLimit: key.tokenLimit,
    requestLimit: key.requestLimit,
    unlimitedRequests: key.requestLimit === 0,
  }
  editOpen.value = true
}
async function copyOnce(text: string, ok: string) {
  if (copying.value) return
  copying.value = true
  try {
    notify((await copyText(text)) ? ok : 'Unable to copy')
  } finally {
    copying.value = false
  }
}
function copyId() {
  if (!editDraft.value) return
  void copyOnce(editDraft.value.id, 'Key ID copied')
}
function copySecrets() {
  void copyOnce(shareBlock(), 'Copied')
}
async function confirmStatus() {
  const target = statusTarget.value
  if (!target) return
  const ok = await run(() => props.service.setEnabled(target.id, target.status === 'disabled', props.scenario), target.status === 'disabled' ? 'Key enabled' : 'Key disabled')
  if (ok) statusTarget.value = null
}
async function confirmDelete() {
  const target = confirming.value
  if (!target) return
  const ok = await run(() => props.service.remove(target.id, props.scenario), 'Key deleted')
  if (!ok) return
  selection.drop([target.id])
  confirming.value = null
}
async function confirmBulkDelete() {
  const ok = await applyBulk(() => props.service.bulkDelete(selection.selected.value, props.scenario), 'Keys deleted')
  if (ok) deleteOpen.value = false
}
async function confirmExpiry() {
  const ok = await applyBulk(() => props.service.bulkUpdate(selection.selected.value, expiryNever.value ? { neverExpires: true } : { ttlSeconds: ttlSecondsFromLocal(expiryLocal.value) }, props.scenario), 'Expiry updated')
  if (ok) expiryOpen.value = false
}
function shareBlock() {
  const endpoint = createdTunnel.value?.endpoint
  if (!endpoint) return secrets.value.join('\n')
  return secrets.value.map((secret) => `Endpoint: ${endpoint}\nKey: ${secret}\n`).join('\n')
}
async function createKeys() {
  if (acting.value) return
  if (!createDraft.value.groupId) {
    notify('Choose a model group.')
    return
  }
  await guard(async () => {
    try {
      const created = await props.service.create({ ...createDraft.value, requestId: createRequestId.value }, props.scenario)
      secrets.value = created.secrets
      createdTunnel.value = created.tunnel
      rememberGroup(createDraft.value.groupId)
      createOpen.value = false
      secretOpen.value = true
      notify(`Created ${secrets.value.length} key${secrets.value.length === 1 ? '' : 's'}`)
      await load()
    } catch (err) {
      notify(err instanceof Error ? err.message : 'Create failed')
    }
  })
}
async function saveEdit() {
  if (acting.value) return
  if (!editDraft.value) return
  if (!editDraft.value.groupId) {
    notify('Choose a model group.')
    return
  }
  if (!editDraft.value.neverExpires && !editDraft.value.expiresLocal) {
    notify('Choose an expiry or turn on Never expires.')
    return
  }
  await run(async () => {
    await props.service.update(editDraft.value as EditDraft, props.scenario)
    rememberGroup(editDraft.value?.groupId || '')
    editOpen.value = false
  }, 'Changes saved')
}
async function applyLimits() {
  if (acting.value) return
  const patch: { tokenLimit?: number; unlimitedTokens?: boolean; requestLimit?: number; unlimitedRequests?: boolean } = {}
  if (limitTokens.value) {
    if (unlimitedTokens.value) patch.unlimitedTokens = true
    else patch.tokenLimit = tokenLimit.value
  }
  if (limitRequests.value) {
    if (unlimitedRequests.value) patch.unlimitedRequests = true
    else patch.requestLimit = requestLimit.value
  }
  const ok = await applyBulk(() => props.service.bulkUpdate(selection.selected.value, patch, props.scenario), 'Limits updated')
  if (ok) limitsOpen.value = false
}
async function applyBulk(action: () => Promise<number>, message: string) {
  if (acting.value) return false
  const ids = [...selection.selected.value]
  if (!ids.length) return false
  if (ids.length > 500) {
    notify('Choose 500 keys or fewer.')
    return false
  }
  return run(async () => {
    await action()
    selection.clear()
  }, message)
}
watch(() => props.scenario, () => { void load() }, { immediate: true })
watch(refreshTick, () => { void load() })
watch(partner, () => {
  if (page.value !== 1) page.value = 1
  else void load()
})
</script>

<template>
  <div>
    <PageHeader kicker="Access" title="Keys" copy="Issue keys into a model group. Secrets appear only after creation or rotation.">
      <AppButton kind="primary" :disabled="!groups.length || acting" @click="acting ? undefined : openCreate()">{{ acting ? 'Working...' : 'Create keys' }}</AppButton>
    </PageHeader>
    <p v-if="!loading && !groups.length" class="notice mb-4">Create a model group before issuing keys.</p>
    <p v-if="error" class="alert mb-4" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" :disabled="loading" @click="load">{{ loading ? 'Loading...' : 'Retry' }}</button></p>
    <section class="panel">
      <FilterBar>
        <AppTextInput v-model="query" placeholder="Search label, customer, or ID" />
        <AppSelect v-model="status" :options="statusOptions" label="Filter by status" />
        <GroupSelect v-model="group" :groups="groups" include-all include-none label="Filter by group" />
        <AppSelect v-model="partner" :options="partnerOptions" label="Filter by partner" />
      </FilterBar>
      <p v-if="loading && !keys.length" role="status">Loading keys…</p>
      <template v-else>
        <div class="table-shell only-desktop">
          <div class="table-scroll">
            <table class="data-table">
              <caption class="sr-only">Customer keys</caption>
              <thead>
                <tr>
                  <th class="check-col">
                    <span class="check-icon-only"><AppCheckbox :checked="selection.pageChecked(pageIds)" label="Select this page" @toggle="selection.togglePage(pageIds)" /></span>
                  </th>
                  <th>Key</th><th>Customer</th><th>Group</th><th>Partner</th><th>Status</th><th class="text-right">Requests</th><th>Usage</th><th>Expires</th><th class="actions">Actions</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="key in keys" :key="key.id">
                  <td>
                    <span class="check-icon-only"><AppCheckbox :checked="selection.has(key.id)" :label="`Select ${key.label}`" @toggle="selection.toggle(key.id)" /></span>
                  </td>
                  <td><strong class="num clip block max-w-44 text-[13px] font-medium">{{ key.label }}</strong><div class="sub num">{{ key.display }}</div></td>
                  <td>{{ key.customer || '—' }}</td>
                  <td>{{ groupLabel(key.groupId) }}</td>
                  <td>
                    <template v-if="key.partnerId || key.partnerRef">
                      <span>{{ partnerLabel(key.partnerId) }}</span>
                      <div v-if="key.partnerRef" class="sub num">{{ key.partnerRef }}</div>
                    </template>
                    <span v-else>—</span>
                  </td>
                  <td><StatusChip interactive :value="key.status" @select="statusTarget = key" /><div v-if="reasons(key)" class="sub text-[var(--warn)]">{{ reasons(key) }}</div></td>
                  <td class="num text-right text-xs">{{ quota(key.requestQuotaUsed, key.requestLimit) }}<div class="sub"><RequestCounts :succeeded="key.requestSuccessCount" :failed="key.requestFailureCount" :canceled="key.requestCanceledCount" :rejected="key.requestRejectedCount" /></div></td>
                  <td class="num usage-cell text-xs">
                    <template v-if="key.tokenLimit"><span>{{ formatNumber(key.tokensUsed) }}</span><span class="usage-limit">/ {{ formatNumber(key.tokenLimit) }}</span></template>
                    <template v-else>{{ formatNumber(key.tokensUsed) }} / ∞</template>
                  </td>
                  <td><template v-if="key.expiresAt"><time class="block text-sm" :datetime="key.expiresAt">{{ splitWhen(key.expiresAt).date }}</time><span class="sub">{{ splitWhen(key.expiresAt).time }}</span></template><template v-else>Never</template></td>
                  <td class="actions">
                    <div class="flex gap-1">
                      <AppButton small :disabled="acting" @click="openEdit(key)">Edit</AppButton>
                      <AppButton small :disabled="acting" @click="run(async () => { secrets = [await service.rotate(key.id, scenario)]; createdTunnel = null; secretOpen = true }, 'Key rotated')">{{ acting ? 'Working...' : 'Rotate' }}</AppButton>
                      <AppButton small kind="danger" :disabled="acting" @click="acting ? undefined : (confirming = key)">Delete</AppButton>
                    </div>
                  </td>
                </tr>
                <tr v-if="!keys.length"><td colspan="10" class="py-10 text-center text-[var(--ink-soft)]">No matching keys.</td></tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="card-list only-mobile">
          <article v-for="key in keys" :key="key.id" class="record">
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <AppCheckbox :checked="selection.has(key.id)" :label="key.label" @toggle="selection.toggle(key.id)" />
                <span class="num sub block">{{ key.display }}</span>
              </div>
              <StatusChip interactive :value="key.status" @select="statusTarget = key" />
            </div>
            <div class="mt-3 grid grid-cols-2 gap-3">
              <div class="kv"><span>Customer</span><strong>{{ key.customer || '—' }}</strong></div>
              <div class="kv"><span>Group</span><strong>{{ groupLabel(key.groupId) }}</strong></div>
              <div v-if="key.partnerId || key.partnerRef" class="kv col-span-2"><span>Partner</span><strong>{{ partnerLabel(key.partnerId) }}<span v-if="key.partnerRef" class="sub num"> · {{ key.partnerRef }}</span></strong></div>
              <div class="kv col-span-2"><span>Requests</span><strong class="num">{{ quota(key.requestQuotaUsed, key.requestLimit) }}</strong></div>
            </div>
            <div class="mt-3 flex flex-wrap gap-2">
              <AppButton small :disabled="acting" @click="openEdit(key)">Edit</AppButton>
              <AppButton small :disabled="acting" @click="run(async () => { secrets = [await service.rotate(key.id, scenario)]; createdTunnel = null; secretOpen = true }, 'Key rotated')">{{ acting ? 'Working...' : 'Rotate' }}</AppButton>
              <AppButton small kind="danger" :disabled="acting" @click="acting ? undefined : (confirming = key)">Delete</AppButton>
            </div>
          </article>
          <p v-if="!keys.length" class="record text-center text-sm text-[var(--ink-soft)]">No matching keys.</p>
        </div>
        <Pager :note="pageRange(page, keys.length, total, service.pageSize)" :page="page" :has-more="hasMore" :disabled="loading || acting" @prev="page -= 1" @next="page += 1" />
        <BulkBar
          v-if="selection.count.value"
          v-model:group-id="bulkGroup"
          :count="selection.count.value"
          :groups="groups"
          :disabled="loading || acting"
          :pending="acting"
          @apply-group="applyBulk(() => service.bulkUpdate(selection.selected.value, { groupId: bulkGroup }, scenario), 'Group updated')"
          @enable="applyBulk(() => service.bulkUpdate(selection.selected.value, { status: 'active' }, scenario), 'Keys enabled')"
          @disable="applyBulk(() => service.bulkUpdate(selection.selected.value, { status: 'disabled' }, scenario), 'Keys disabled')"
          @expiry="expiryOpen = true"
          @limits="limitsOpen = true"
          @delete="deleteOpen = true"
          @clear="selection.clear()"
        />
      </template>
    </section>

    <AppSheet :open="createOpen" title="Create keys" :locked="acting" @close="createOpen = false">
      <p class="section-copy">Create one key or a batch of up to 500. A group is required. The secret is shown once.</p>
      <form class="mt-4 grid gap-3" @submit.prevent="createKeys">
        <AppField label="Label" :hint="createDraft.count > 1 ? 'Batch labels get a short unique suffix.' : 'Saved exactly as typed.'">
          <div class="flex gap-2"><AppTextInput v-model="createDraft.label" :maxlength="200" placeholder="Any name" /><AppButton type="button" @click="createDraft.label = randomLabel()">Generate</AppButton></div>
        </AppField>
        <AppField label="Model group" hint="Required. There is no ungrouped option for new keys.">
          <GroupSelect v-model="createDraft.groupId" :groups="groups" label="Model group" />
        </AppField>
        <AppField label="Customer"><AppTextInput v-model="createDraft.customer" placeholder="Customer name" /></AppField>
        <div class="grid gap-3 sm:grid-cols-2">
          <AppField label="Quantity"><AppNumberInput v-model="createDraft.count" :min="1" :max="500" /></AppField>
          <AppField label="Token limit"><AppNumberInput v-model="createDraft.tokenLimit" :disabled="createDraft.unlimitedTokens" /></AppField>
        </div>
        <AppField label="Expires at"><AppDateTime v-model="createDraft.expiresLocal" :disabled="createDraft.neverExpires" /></AppField>
        <AppSwitch :on="createDraft.neverExpires" text="Never expires" label="Never expires" @toggle="createDraft.neverExpires = !createDraft.neverExpires" />
        <AppCheckbox :checked="createDraft.unlimitedTokens" label="Unlimited tokens" @toggle="createDraft.unlimitedTokens = !createDraft.unlimitedTokens" />
        <AppField label="Request limit"><AppNumberInput v-model="createDraft.requestLimit" :disabled="createDraft.unlimitedRequests" /></AppField>
        <AppCheckbox :checked="createDraft.unlimitedRequests" label="Unlimited requests" @toggle="createDraft.unlimitedRequests = !createDraft.unlimitedRequests" />
        <AppField label="Internal note"><AppTextInput v-model="createDraft.note" placeholder="Optional note" /></AppField>
        <AppSwitch :on="Boolean(createDraft.tunnelEnabled)" text="Create tunnel too" label="Create tunnel too" @toggle="createDraft.tunnelEnabled = !createDraft.tunnelEnabled" />
        <AppField v-if="createDraft.tunnelEnabled" label="Tunnel target" hint="Leave blank to choose automatically. A quick tunnel URL changes when the tunnel restarts.">
          <AppTextInput v-model="createDraft.tunnelTarget" placeholder="Blank = this CPA address" />
        </AppField>
        <AppButton kind="primary" type="submit" :disabled="acting || !createDraft.groupId">{{ acting ? 'Creating...' : `Create ${createDraft.count || 1} key${createDraft.count === 1 ? '' : 's'}` }}</AppButton>
      </form>
    </AppSheet>

    <AppSheet :open="editOpen" title="Edit key" :locked="acting" @close="editOpen = false">
      <template v-if="editDraft">
        <div class="rounded-[10px] border border-[var(--line)] bg-[var(--bg-float)] p-3">
          <div class="field-label">Key ID</div>
          <div class="mt-1 flex items-start justify-between gap-3"><code class="num text-xs">{{ editDraft.id }}</code><AppButton small :disabled="copying" @click="copyId">{{ copying ? 'Copying...' : 'Copy' }}</AppButton></div>
        </div>
        <div class="mt-3 grid gap-3">
          <AppField label="Label"><AppTextInput v-model="editDraft.label" :maxlength="200" /></AppField>
          <AppField label="Customer"><AppTextInput v-model="editDraft.customer" /></AppField>
          <AppField label="Model group"><GroupSelect v-model="editDraft.groupId" :groups="groups" label="Model group" /></AppField>
          <AppField label="Token limit"><AppNumberInput v-model="editDraft.tokenLimit" /></AppField>
          <AppField label="Request limit"><AppNumberInput v-model="editDraft.requestLimit" :disabled="editDraft.unlimitedRequests" /></AppField>
          <AppCheckbox :checked="editDraft.unlimitedRequests" label="Unlimited requests" @toggle="editDraft.unlimitedRequests = !editDraft.unlimitedRequests" />
          <AppField label="Expires at"><AppDateTime v-model="editDraft.expiresLocal" :disabled="editDraft.neverExpires" /></AppField>
          <AppSwitch :on="editDraft.neverExpires" text="Never expires" label="Never expires" @toggle="editDraft.neverExpires = !editDraft.neverExpires" />
          <div>
            <span class="field-label">Status</span>
            <AppSegmented class="mt-1.5" :model-value="editDraft.status" label="Key status" :options="[{ value: 'active', label: 'Enabled' }, { value: 'disabled', label: 'Disabled' }]" @update:model-value="editDraft.status = $event as 'active' | 'disabled'" />
          </div>
          <div class="flex flex-wrap gap-2">
            <AppButton kind="primary" :disabled="acting" @click="saveEdit">{{ acting ? 'Saving...' : 'Save' }}</AppButton>
            <AppButton :disabled="acting" @click="run(() => service.resetUsage(editDraft!.id, scenario), 'Token usage reset')">{{ acting ? 'Working...' : 'Reset usage' }}</AppButton>
          </div>
        </div>
      </template>
    </AppSheet>

    <AppModal :open="secretOpen" title="Copy the secret" @close="secretOpen = false">
      <p class="section-copy">This secret is shown once. Store it before closing.</p>
      <p v-if="createdTunnel?.error" class="alert mt-3" role="alert">Keys were created, but the tunnel was not. {{ createdTunnel.error }}</p>
      <pre class="secret mt-3">{{ shareBlock() }}</pre>
      <p v-if="createdTunnel?.endpoint" class="sub mt-2">A quick tunnel URL changes when the tunnel restarts.</p>
      <AppButton class="mt-3" :disabled="copying" @click="copySecrets">{{ copying ? 'Copying...' : (createdTunnel?.endpoint ? 'Copy all' : 'Copy secrets') }}</AppButton>
    </AppModal>
    <AppModal :open="Boolean(statusTarget)" kicker="Confirm" :title="statusTarget?.status === 'disabled' ? 'Enable this key?' : 'Disable this key?'" :locked="acting" @close="statusTarget = null">
      <p class="section-copy">“{{ statusTarget?.label }}” will {{ statusTarget?.status === 'disabled' ? 'be allowed to call again' : 'stop accepting calls until you enable it' }}.</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton :disabled="acting" @click="statusTarget = null">Cancel</AppButton>
        <AppButton kind="primary" :disabled="acting" @click="confirmStatus">{{ acting ? 'Working...' : 'Confirm' }}</AppButton>
      </div>
    </AppModal>
    <AppModal :open="Boolean(confirming)" kicker="Confirm" title="Delete this key?" :locked="acting" @close="confirming = null">
      <p class="section-copy">“{{ confirming?.label }}” will stop working immediately.</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton :disabled="acting" @click="confirming = null">Cancel</AppButton>
        <AppButton kind="danger" :disabled="acting" @click="confirmDelete">{{ acting ? 'Deleting...' : 'Delete key' }}</AppButton>
      </div>
    </AppModal>
    <AppModal :open="deleteOpen" kicker="Confirm" :title="`Delete ${selection.count.value} keys?`" :locked="acting" @close="deleteOpen = false">
      <p class="section-copy">{{ selection.count.value }} selected keys will stop working immediately. This cannot be undone.</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton :disabled="acting" @click="deleteOpen = false">Cancel</AppButton>
        <AppButton kind="danger" :disabled="acting" @click="confirmBulkDelete">{{ acting ? 'Deleting...' : `Delete ${selection.count.value} keys` }}</AppButton>
      </div>
    </AppModal>
    <AppModal :open="expiryOpen" title="Set expiry" :locked="acting" @close="expiryOpen = false">
      <p class="section-copy">Applies to {{ selection.count.value }} selected keys. Other fields stay unchanged.</p>
      <div class="mt-3 grid gap-3">
        <AppField label="Expires at"><AppDateTime v-model="expiryLocal" :disabled="expiryNever" /></AppField>
        <AppSwitch :on="expiryNever" text="Never expires" label="Never expires" @toggle="expiryNever = !expiryNever" />
        <AppButton kind="primary" :disabled="acting" @click="confirmExpiry">{{ acting ? 'Saving...' : 'Apply expiry' }}</AppButton>
      </div>
    </AppModal>
    <AppModal :open="limitsOpen" title="Set limits" :locked="acting" @close="limitsOpen = false">
      <p class="section-copy">Only the checked limits are sent for {{ selection.count.value }} keys.</p>
      <div class="mt-3 grid gap-3">
        <AppCheckbox :checked="limitTokens" label="Update token limit" @toggle="limitTokens = !limitTokens" />
        <AppNumberInput v-model="tokenLimit" :disabled="!limitTokens || unlimitedTokens" />
        <AppCheckbox :checked="unlimitedTokens" label="Unlimited tokens" :disabled="!limitTokens" @toggle="unlimitedTokens = !unlimitedTokens" />
        <AppCheckbox :checked="limitRequests" label="Update request limit" @toggle="limitRequests = !limitRequests" />
        <AppNumberInput v-model="requestLimit" :disabled="!limitRequests || unlimitedRequests" />
        <AppCheckbox :checked="unlimitedRequests" label="Unlimited requests" :disabled="!limitRequests" @toggle="unlimitedRequests = !unlimitedRequests" />
        <AppButton kind="primary" :disabled="acting || (!limitTokens && !limitRequests)" @click="applyLimits">{{ acting ? 'Saving...' : 'Apply limits' }}</AppButton>
      </div>
    </AppModal>
  </div>
</template>
