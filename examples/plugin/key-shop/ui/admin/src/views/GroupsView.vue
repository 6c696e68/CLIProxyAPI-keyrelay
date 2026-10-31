<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import ModelPicker from '@/components/ModelPicker.vue'
import PageHeader from '@/components/PageHeader.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppField from '@/components/ui/AppField.vue'
import AppModal from '@/components/ui/AppModal.vue'
import AppSheet from '@/components/ui/AppSheet.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import { useLoadGuard } from '@/composables/useLoadGuard'
import { useSubmitGuard } from '@/composables/useSubmitGuard'
import { bindViewBusy, notifyKey, refreshKey } from '@/composables/useToast'
import { ApiError } from '@/lib/api-error'
import { formatNumber } from '@/lib/format'
import { groupModelProblems } from '@/lib/group-models'
import type { AdminService, ModelEntry, ModelGroup, Policy, Scenario } from '@/types'

const props = withDefaults(defineProps<{ service: AdminService; scenario?: Scenario }>(), { scenario: 'ready' })
const notify = inject(notifyKey, () => {})
const refreshTick = inject(refreshKey, ref(0))
const groups = ref<ModelGroup[]>([])
const policy = ref<Policy | null>(null)
const catalog = ref<string[]>([])
const error = ref('')
const { loading, load: runLoad } = useLoadGuard()
const opening = ref(false)
const editorOpen = ref(false)
const editing = ref<ModelGroup | null>(null)
const name = ref('')
const description = ref('')
const models = ref<ModelEntry[]>([])
const { pending: saving, guard: guardSave } = useSubmitGuard()
const { pending: deleting, guard: guardDelete } = useSubmitGuard()
bindViewBusy(() => loading.value || saving.value || deleting.value)
const pendingDelete = ref<ModelGroup | null>(null)
const deleteError = ref('')

const customPolicy = computed(() => policy.value?.loaded && policy.value.mode === 'custom')
const aliasProblems = computed(() => groupModelProblems(models.value))
const aliasInvalid = computed(() => Object.keys(aliasProblems.value).length > 0)
const nameTaken = computed(() => {
  const wanted = name.value.trim().toLowerCase()
  return Boolean(wanted) && groups.value.some((group) => group.id !== editing.value?.id && group.name.trim().toLowerCase() === wanted)
})
const editingInUse = computed(() => (editing.value?.keyCount ?? 0) > 0)

// clientNames lists what customers type: the alias when set, else the upstream name.
function clientNames(group: ModelGroup): string[] {
  return group.models.map((item) => item.alias.trim() || item.name)
}
function preview(group: ModelGroup): string {
  const names = clientNames(group)
  const shown = names.slice(0, 3).join(', ')
  return names.length > 3 ? `${shown} +${names.length - 3}` : shown
}

function load() {
  return runLoad(async () => {
  if (props.scenario === 'loading') {
    groups.value = []
    return
  }
  error.value = ''
  try {
    const [nextGroups, nextPolicy] = await Promise.all([
      props.service.groups(props.scenario),
      props.service.policy(props.scenario),
    ])
    groups.value = nextGroups
    policy.value = nextPolicy
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Request failed'
  }
  })
}

async function openCreate() {
  if (opening.value) return
  opening.value = true
  editing.value = null
  name.value = ''
  description.value = ''
  models.value = []
  editorOpen.value = true
  catalog.value = await props.service.catalog(props.scenario).catch(() => [])
  opening.value = false
}
async function openEdit(group: ModelGroup) {
  if (group.isSystem || opening.value) return
  opening.value = true
  editing.value = group
  name.value = group.name
  description.value = group.description
  models.value = group.models.map((item) => ({ name: item.name, alias: item.alias }))
  editorOpen.value = true
  catalog.value = await props.service.catalog(props.scenario).catch(() => [])
  opening.value = false
}
async function save() {
  if (saving.value || deleting.value) return
  await guardSave(async () => {
  try {
    await props.service.saveGroup({
      id: editing.value?.id,
      name: name.value,
      description: description.value,
      allModels: false,
      models: models.value,
    }, props.scenario)
    editorOpen.value = false
    notify(editing.value ? 'Group saved' : 'Group created')
    await load()
  } catch (err) {
    notify(err instanceof Error ? err.message : 'Group could not be saved')
  }
  })
}
async function remove() {
  const target = pendingDelete.value
  if (!target || saving.value) return
  deleteError.value = ''
  await guardDelete(async () => {
    try {
      await props.service.removeGroup(target.id, props.scenario)
      notify('Group deleted')
      pendingDelete.value = null
      await load()
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        const count = err.keyCount
        deleteError.value = count == null
          ? err.message
          : `This group is used by ${count} key${count === 1 ? '' : 's'} and cannot be deleted.`
        return
      }
      deleteError.value = err instanceof Error ? err.message : 'Group could not be deleted'
    }
  })
}
watch(() => props.scenario, () => { void load() }, { immediate: true })
watch(refreshTick, () => { void load() })
</script>

<template>
  <div>
    <PageHeader kicker="Access" title="Model groups" copy="Each new key must belong to one group. A selected model can have a client alias.">
      <AppButton kind="primary" :disabled="opening" @click="openCreate">{{ opening ? 'Loading...' : 'New group' }}</AppButton>
    </PageHeader>
    <p v-if="customPolicy" class="notice mb-4" role="status">Global model policy is custom. A key can call a model only when both this group and the global policy allow it.</p>
    <p v-if="error" class="alert mb-4" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" :disabled="loading" @click="load">{{ loading ? 'Loading...' : 'Retry' }}</button></p>
    <section class="panel">
      <p v-if="loading && !groups.length" role="status">Loading groups…</p>
      <template v-else>
        <div class="table-shell only-desktop">
          <div class="table-scroll">
            <table class="data-table fit">
              <caption class="sr-only">Model groups</caption>
              <thead><tr><th>Name</th><th>Models</th><th class="text-right">Keys</th><th class="actions">Actions</th></tr></thead>
              <tbody>
                <tr v-for="group in groups" :key="group.id">
                  <td>
                    <strong>{{ group.name }}</strong>
                    <div class="sub">{{ group.description || group.id }}</div>
                  </td>
                  <td class="text-xs">
                    <template v-if="group.allModels">All models</template>
                    <template v-else>
                      <strong class="num">{{ formatNumber(group.models.length) }}</strong>
                      <div class="sub num" :title="clientNames(group).join(', ')">{{ preview(group) }}</div>
                    </template>
                  </td>
                  <td class="num text-right">{{ formatNumber(group.keyCount) }}</td>
                  <td class="actions">
                    <div class="flex gap-1">
                      <AppButton small :disabled="group.isSystem || opening" @click="openEdit(group)">{{ opening ? 'Loading...' : 'Edit' }}</AppButton>
                      <AppButton small kind="danger" :disabled="group.isSystem || deleting || saving" @click="(deleting || saving) ? undefined : (pendingDelete = group, deleteError = '')">Delete</AppButton>
                    </div>
                  </td>
                </tr>
                <tr v-if="!groups.length"><td colspan="4" class="py-10 text-center text-[var(--ink-soft)]">No model groups yet.</td></tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="card-list only-mobile">
          <article v-for="group in groups" :key="group.id" class="record">
            <strong>{{ group.name }}</strong>
            <p class="sub">{{ group.description || 'No description' }}</p>
            <p v-if="!group.allModels && group.models.length" class="sub num mt-1" :title="clientNames(group).join(', ')">{{ preview(group) }}</p>
            <div class="mt-3 grid grid-cols-2 gap-3">
              <div class="kv"><span>Models</span><strong class="num">{{ group.allModels ? 'All' : formatNumber(group.models.length) }}</strong></div>
              <div class="kv"><span>Keys</span><strong class="num">{{ formatNumber(group.keyCount) }}</strong></div>
            </div>
            <div class="mt-3 flex gap-2">
              <AppButton small :disabled="group.isSystem || opening" @click="openEdit(group)">{{ opening ? 'Loading...' : 'Edit' }}</AppButton>
              <AppButton small kind="danger" :disabled="group.isSystem || deleting || saving" @click="(deleting || saving) ? undefined : (pendingDelete = group, deleteError = '')">Delete</AppButton>
            </div>
          </article>
          <p v-if="!groups.length" class="record text-center text-sm text-[var(--ink-soft)]">No model groups yet.</p>
        </div>
      </template>
    </section>

    <AppSheet wide :open="editorOpen" :title="editing ? 'Edit group' : 'New group'" :locked="saving || deleting" @close="editorOpen = false">
      <form class="grid gap-3" @submit.prevent="save">
        <p v-if="editingInUse" class="notice" role="status">{{ editing?.keyCount }} key{{ editing?.keyCount === 1 ? '' : 's' }} use this group. Changes apply on the next request. Removing a model or changing an alias makes the old name fail with 403 for those keys.</p>
        <AppField label="Name" hint="1–80 characters. The built-in All models group cannot be edited." :error="nameTaken ? 'Another group already uses this name.' : ''">
          <AppTextInput v-model="name" :maxlength="80" placeholder="vippro" />
        </AppField>
        <AppField label="Description"><AppTextInput v-model="description" placeholder="Optional" /></AppField>
        <ModelPicker v-model="models" :catalog="catalog" :problems="aliasProblems" />
        <AppButton kind="primary" type="submit" :disabled="saving || deleting || !name.trim() || nameTaken || !models.length || aliasInvalid">{{ saving ? 'Saving...' : (editing ? 'Save group' : 'Create group') }}</AppButton>
      </form>
    </AppSheet>

    <AppModal :open="Boolean(pendingDelete)" kicker="Confirm" title="Delete this group?" :locked="deleting" @close="pendingDelete = null">
      <p class="section-copy">“{{ pendingDelete?.name }}” will be removed.</p>
      <p v-if="(pendingDelete?.keyCount ?? 0) > 0" class="notice mt-3" role="status">{{ pendingDelete?.keyCount }} key{{ pendingDelete?.keyCount === 1 ? '' : 's' }} still use this group. Move them to another group first, or the delete is refused.</p>
      <p v-if="deleteError" class="alert mt-3" role="alert">{{ deleteError }}</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton :disabled="deleting" @click="pendingDelete = null">Cancel</AppButton>
        <AppButton kind="danger" :disabled="deleting" @click="remove">{{ deleting ? 'Deleting...' : 'Delete group' }}</AppButton>
      </div>
    </AppModal>
  </div>
</template>
