<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import GroupSelect from '@/components/GroupSelect.vue'
import PageHeader from '@/components/PageHeader.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppCheckbox from '@/components/ui/AppCheckbox.vue'
import AppField from '@/components/ui/AppField.vue'
import AppModal from '@/components/ui/AppModal.vue'
import AppNumberInput from '@/components/ui/AppNumberInput.vue'
import AppSheet from '@/components/ui/AppSheet.vue'
import AppSwitch from '@/components/ui/AppSwitch.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import { useLoadGuard } from '@/composables/useLoadGuard'
import { useSubmitGuard } from '@/composables/useSubmitGuard'
import { bindViewBusy, notifyKey, refreshKey } from '@/composables/useToast'
import { formatNumber } from '@/lib/format'
import type { AdminService, ModelGroup, Plan, PlanInput, Scenario } from '@/types'

const props = withDefaults(defineProps<{ service: AdminService; scenario?: Scenario }>(), { scenario: 'ready' })
const notify = inject(notifyKey, () => {})
const refreshTick = inject(refreshKey, ref(0))
const plans = ref<Plan[]>([])
const groups = ref<ModelGroup[]>([])
const error = ref('')
const { loading, load: runLoad } = useLoadGuard()
const editorOpen = ref(false)
const editing = ref<Plan | null>(null)
const name = ref('')
const description = ref('')
const groupId = ref('')
const ttlSeconds = ref(30 * 86_400)
const neverExpires = ref(false)
const tokenLimit = ref(1_000_000)
const unlimitedTokens = ref(false)
const requestLimit = ref(0)
const unlimitedRequests = ref(true)
const active = ref(true)
const { pending: saving, guard: guardSave } = useSubmitGuard()
const { pending: deleting, guard: guardDelete } = useSubmitGuard()
const pendingDelete = ref<Plan | null>(null)
const deleteError = ref('')
bindViewBusy(() => loading.value || saving.value || deleting.value)

const nameTaken = computed(() => {
  const wanted = name.value.trim().toLowerCase()
  return Boolean(wanted) && plans.value.some((plan) => plan.id !== editing.value?.id && plan.name.trim().toLowerCase() === wanted)
})
const ttlInvalid = computed(() => !neverExpires.value && ttlSeconds.value <= 0)

function groupLabel(id: string): string {
  return groups.value.find((group) => group.id === id)?.name || id || '—'
}
function lifetime(plan: Plan): string {
  if (plan.neverExpires) return 'Never'
  const seconds = plan.ttlSeconds
  if (seconds >= 86_400) return `${Math.round(seconds / 86_400)}d`
  if (seconds >= 3_600) return `${Math.round(seconds / 3_600)}h`
  return `${seconds}s`
}
function limitLabel(limit: number, unlimited: boolean): string {
  return unlimited || !limit ? 'Unlimited' : formatNumber(limit)
}
function planInput(plan: Plan, nextActive: boolean): PlanInput {
  return {
    id: plan.id,
    name: plan.name,
    description: plan.description,
    groupId: plan.group.id,
    ttlSeconds: plan.ttlSeconds,
    neverExpires: plan.neverExpires,
    tokenLimit: plan.tokenLimit,
    unlimitedTokens: plan.unlimitedTokens,
    requestLimit: plan.requestLimit,
    unlimitedRequests: plan.unlimitedRequests,
    active: nextActive,
  }
}

function load() {
  return runLoad(async () => {
    if (props.scenario === 'loading') {
      plans.value = []
      return
    }
    error.value = ''
    try {
      const [nextPlans, nextGroups] = await Promise.all([
        props.service.plans(props.scenario),
        props.service.groups(props.scenario),
      ])
      plans.value = nextPlans
      groups.value = nextGroups
    } catch (err) {
      error.value = err instanceof Error ? err.message : 'Request failed'
    }
  })
}

function openCreate() {
  if (saving.value || deleting.value) return
  editing.value = null
  name.value = ''
  description.value = ''
  groupId.value = groups.value[0]?.id || ''
  ttlSeconds.value = 30 * 86_400
  neverExpires.value = false
  tokenLimit.value = 1_000_000
  unlimitedTokens.value = false
  requestLimit.value = 0
  unlimitedRequests.value = true
  active.value = true
  editorOpen.value = true
}
function openEdit(plan: Plan) {
  if (saving.value || deleting.value) return
  editing.value = plan
  name.value = plan.name
  description.value = plan.description
  groupId.value = plan.group.id
  ttlSeconds.value = plan.ttlSeconds
  neverExpires.value = plan.neverExpires
  tokenLimit.value = plan.tokenLimit
  unlimitedTokens.value = plan.unlimitedTokens
  requestLimit.value = plan.requestLimit
  unlimitedRequests.value = plan.unlimitedRequests
  active.value = plan.active
  editorOpen.value = true
}

async function save() {
  if (saving.value || deleting.value) return
  if (!name.value.trim() || !groupId.value || ttlInvalid.value) return
  await guardSave(async () => {
    try {
      await props.service.savePlan({
        id: editing.value?.id,
        name: name.value,
        description: description.value,
        groupId: groupId.value,
        ttlSeconds: ttlSeconds.value,
        neverExpires: neverExpires.value,
        tokenLimit: tokenLimit.value,
        unlimitedTokens: unlimitedTokens.value,
        requestLimit: requestLimit.value,
        unlimitedRequests: unlimitedRequests.value,
        active: active.value,
      }, props.scenario)
      editorOpen.value = false
      notify(editing.value ? 'Plan saved' : 'Plan created')
      await load()
    } catch (err) {
      notify(err instanceof Error ? err.message : 'Plan could not be saved')
    }
  })
}

async function toggle(plan: Plan) {
  if (saving.value || deleting.value) return
  await guardSave(async () => {
    try {
      await props.service.savePlan(planInput(plan, !plan.active), props.scenario)
      notify(plan.active ? 'Plan deactivated' : 'Plan activated')
      await load()
    } catch (err) {
      notify(err instanceof Error ? err.message : 'Plan could not change state')
    }
  })
}

async function remove() {
  const target = pendingDelete.value
  if (!target || saving.value) return
  deleteError.value = ''
  await guardDelete(async () => {
    try {
      await props.service.removePlan(target.id, props.scenario)
      notify('Plan deleted')
      pendingDelete.value = null
      await load()
    } catch (err) {
      deleteError.value = err instanceof Error ? err.message : 'Plan could not be deleted'
    }
  })
}

watch(() => props.scenario, () => { void load() }, { immediate: true })
watch(refreshTick, () => { void load() })
</script>

<template>
  <div>
    <PageHeader kicker="Sales" title="Plans" copy="A plan fixes the group, lifetime, and limits for every key a partner mints.">
      <AppButton kind="primary" :disabled="saving || deleting" @click="openCreate">{{ saving ? 'Working...' : 'New plan' }}</AppButton>
    </PageHeader>
    <p v-if="error" class="alert mb-4" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" :disabled="loading" @click="load">{{ loading ? 'Loading...' : 'Retry' }}</button></p>
    <section class="panel">
      <p v-if="loading && !plans.length" role="status">Loading plans…</p>
      <template v-else>
        <div class="table-shell only-desktop">
          <div class="table-scroll">
            <table class="data-table fit">
              <caption class="sr-only">Sale plans</caption>
              <thead><tr><th>Name</th><th>Group</th><th>Lifetime</th><th>Limits</th><th>Status</th><th class="text-right">Revision</th><th class="actions">Actions</th></tr></thead>
              <tbody>
                <tr v-for="plan in plans" :key="plan.id">
                  <td>
                    <strong>{{ plan.name }}</strong>
                    <div class="sub">{{ plan.description || plan.id }}</div>
                  </td>
                  <td>{{ groupLabel(plan.group.id) }}</td>
                  <td>{{ lifetime(plan) }}</td>
                  <td class="text-xs">
                    <div>Tokens: {{ limitLabel(plan.tokenLimit, plan.unlimitedTokens) }}</div>
                    <div class="sub">Requests: {{ limitLabel(plan.requestLimit, plan.unlimitedRequests) }}</div>
                  </td>
                  <td><span class="chip" :class="plan.active ? 'chip-good' : ''"><i />{{ plan.active ? 'Active' : 'Inactive' }}</span></td>
                  <td class="num text-right">{{ plan.revision }}</td>
                  <td class="actions">
                    <div class="flex gap-1">
                      <AppButton small :disabled="saving || deleting" @click="openEdit(plan)">Edit</AppButton>
                      <AppButton small :disabled="saving || deleting" @click="toggle(plan)">{{ plan.active ? 'Deactivate' : 'Activate' }}</AppButton>
                      <AppButton small kind="danger" :disabled="saving || deleting" @click="(saving || deleting) ? undefined : (pendingDelete = plan, deleteError = '')">Delete</AppButton>
                    </div>
                  </td>
                </tr>
                <tr v-if="!plans.length"><td colspan="7" class="py-10 text-center text-[var(--ink-soft)]">No plans yet.</td></tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="card-list only-mobile">
          <article v-for="plan in plans" :key="plan.id" class="record">
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <strong>{{ plan.name }}</strong>
                <p class="sub">{{ plan.description || 'No description' }}</p>
              </div>
              <span class="chip" :class="plan.active ? 'chip-good' : ''"><i />{{ plan.active ? 'Active' : 'Inactive' }}</span>
            </div>
            <div class="mt-3 grid grid-cols-2 gap-3">
              <div class="kv"><span>Group</span><strong>{{ groupLabel(plan.group.id) }}</strong></div>
              <div class="kv"><span>Lifetime</span><strong>{{ lifetime(plan) }}</strong></div>
              <div class="kv"><span>Tokens</span><strong>{{ limitLabel(plan.tokenLimit, plan.unlimitedTokens) }}</strong></div>
              <div class="kv"><span>Requests</span><strong>{{ limitLabel(plan.requestLimit, plan.unlimitedRequests) }}</strong></div>
              <div class="kv"><span>Revision</span><strong class="num">{{ plan.revision }}</strong></div>
            </div>
            <div class="mt-3 flex flex-wrap gap-2">
              <AppButton small :disabled="saving || deleting" @click="openEdit(plan)">Edit</AppButton>
              <AppButton small :disabled="saving || deleting" @click="toggle(plan)">{{ plan.active ? 'Deactivate' : 'Activate' }}</AppButton>
              <AppButton small kind="danger" :disabled="saving || deleting" @click="(saving || deleting) ? undefined : (pendingDelete = plan, deleteError = '')">Delete</AppButton>
            </div>
          </article>
          <p v-if="!plans.length" class="record text-center text-sm text-[var(--ink-soft)]">No plans yet.</p>
        </div>
      </template>
    </section>

    <AppSheet wide :open="editorOpen" :title="editing ? 'Edit plan' : 'New plan'" :locked="saving || deleting" @close="editorOpen = false">
      <form class="grid gap-3" @submit.prevent="save">
        <p v-if="editing" class="notice" role="status">Editing replaces every field. A content change bumps the revision, and a mint that still names the old revision is refused.</p>
        <AppField label="Name" :error="nameTaken ? 'Another plan already uses this name.' : ''">
          <AppTextInput v-model="name" :maxlength="80" placeholder="Pro 30d" />
        </AppField>
        <AppField label="Description"><AppTextInput v-model="description" placeholder="Optional" /></AppField>
        <AppField label="Model group" hint="Required. The group must exist.">
          <GroupSelect v-model="groupId" :groups="groups" label="Model group" />
        </AppField>
        <div class="grid gap-3 sm:grid-cols-2">
          <AppField label="TTL seconds" :hint="neverExpires ? 'Ignored while Never expires is on.' : '604800 = 7 days.'">
            <AppNumberInput v-model="ttlSeconds" :disabled="neverExpires" :min="0" />
          </AppField>
          <div class="flex items-end pb-1">
            <AppSwitch :on="neverExpires" text="Never expires" label="Never expires" @toggle="neverExpires = !neverExpires" />
          </div>
        </div>
        <p v-if="ttlInvalid" class="field-error" role="alert">TTL must be greater than zero unless the plan never expires.</p>
        <div class="grid gap-3 sm:grid-cols-2">
          <AppField label="Token limit"><AppNumberInput v-model="tokenLimit" :disabled="unlimitedTokens" /></AppField>
          <div class="flex items-end pb-1"><AppCheckbox :checked="unlimitedTokens" label="Unlimited tokens" @toggle="unlimitedTokens = !unlimitedTokens" /></div>
          <AppField label="Request limit"><AppNumberInput v-model="requestLimit" :disabled="unlimitedRequests" /></AppField>
          <div class="flex items-end pb-1"><AppCheckbox :checked="unlimitedRequests" label="Unlimited requests" @toggle="unlimitedRequests = !unlimitedRequests" /></div>
        </div>
        <div class="flex items-center gap-3">
          <AppSwitch :on="active" text="Active" label="Plan active" @toggle="active = !active" />
          <span class="field-hint">Inactive plans are hidden from partners and refuse new mints.</span>
        </div>
        <AppButton kind="primary" type="submit" :disabled="saving || deleting || !name.trim() || !groupId || nameTaken || ttlInvalid">{{ saving ? 'Saving...' : (editing ? 'Save plan' : 'Create plan') }}</AppButton>
      </form>
    </AppSheet>

    <AppModal :open="Boolean(pendingDelete)" kicker="Confirm" title="Delete this plan?" :locked="deleting" @close="pendingDelete = null">
      <p class="section-copy">“{{ pendingDelete?.name }}” will be removed. A plan with partner orders cannot be deleted.</p>
      <p v-if="deleteError" class="alert mt-3" role="alert">{{ deleteError }}</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton :disabled="deleting" @click="pendingDelete = null">Cancel</AppButton>
        <AppButton kind="danger" :disabled="deleting" @click="remove">{{ deleting ? 'Deleting...' : 'Delete plan' }}</AppButton>
      </div>
    </AppModal>
  </div>
</template>
