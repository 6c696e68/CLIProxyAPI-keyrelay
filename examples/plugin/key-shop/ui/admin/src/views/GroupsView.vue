<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import ModelPicker from '@/components/ModelPicker.vue'
import PageHeader from '@/components/PageHeader.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppField from '@/components/ui/AppField.vue'
import AppModal from '@/components/ui/AppModal.vue'
import AppSheet from '@/components/ui/AppSheet.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import { notifyKey, refreshKey } from '@/composables/useToast'
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
const loading = ref(false)
const editorOpen = ref(false)
const editing = ref<ModelGroup | null>(null)
const name = ref('')
const description = ref('')
const models = ref<ModelEntry[]>([])
const saving = ref(false)
const pendingDelete = ref<ModelGroup | null>(null)
const deleteError = ref('')

const customPolicy = computed(() => policy.value?.loaded && policy.value.mode === 'custom')
const aliasProblems = computed(() => groupModelProblems(models.value))
const aliasInvalid = computed(() => Object.keys(aliasProblems.value).length > 0)

async function load() {
  if (props.scenario === 'loading') {
    groups.value = []
    return
  }
  loading.value = true
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
  } finally {
    loading.value = false
  }
}

async function openCreate() {
  editing.value = null
  name.value = ''
  description.value = ''
  models.value = []
  editorOpen.value = true
  catalog.value = await props.service.catalog(props.scenario).catch(() => [])
}
async function openEdit(group: ModelGroup) {
  if (group.isSystem) return
  editing.value = group
  name.value = group.name
  description.value = group.description
  models.value = group.models.map((item) => ({ name: item.name, alias: item.alias }))
  editorOpen.value = true
  catalog.value = await props.service.catalog(props.scenario).catch(() => [])
}
async function save() {
  saving.value = true
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
  } finally {
    saving.value = false
  }
}
async function remove() {
  if (!pendingDelete.value) return
  deleteError.value = ''
  try {
    await props.service.removeGroup(pendingDelete.value.id, props.scenario)
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
}
watch(() => props.scenario, () => { void load() }, { immediate: true })
watch(refreshTick, () => { void load() })
</script>

<template>
  <div>
    <PageHeader kicker="Access" title="Model groups" copy="Each new key must belong to one group. A selected model can have a client alias.">
      <AppButton kind="primary" @click="openCreate">New group</AppButton>
    </PageHeader>
    <p v-if="customPolicy" class="notice mb-4" role="status">Global model policy is custom. A key can call a model only when both this group and the global policy allow it.</p>
    <p v-if="error" class="alert mb-4" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" @click="load">Retry</button></p>
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
                  <td class="num text-xs">{{ group.allModels ? 'All models' : formatNumber(group.models.length) }}</td>
                  <td class="num text-right">{{ formatNumber(group.keyCount) }}</td>
                  <td class="actions">
                    <div class="flex gap-1">
                      <AppButton small :disabled="group.isSystem" @click="openEdit(group)">Edit</AppButton>
                      <AppButton small kind="danger" :disabled="group.isSystem" @click="pendingDelete = group; deleteError = ''">Delete</AppButton>
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
            <div class="mt-3 grid grid-cols-2 gap-3">
              <div class="kv"><span>Models</span><strong class="num">{{ group.allModels ? 'All' : formatNumber(group.models.length) }}</strong></div>
              <div class="kv"><span>Keys</span><strong class="num">{{ formatNumber(group.keyCount) }}</strong></div>
            </div>
            <div class="mt-3 flex gap-2">
              <AppButton small :disabled="group.isSystem" @click="openEdit(group)">Edit</AppButton>
              <AppButton small kind="danger" :disabled="group.isSystem" @click="pendingDelete = group; deleteError = ''">Delete</AppButton>
            </div>
          </article>
          <p v-if="!groups.length" class="record text-center text-sm text-[var(--ink-soft)]">No model groups yet.</p>
        </div>
      </template>
    </section>

    <AppSheet wide :open="editorOpen" :title="editing ? 'Edit group' : 'New group'" @close="editorOpen = false">
      <form class="grid gap-3" @submit.prevent="save">
        <AppField label="Name" hint="1–80 characters. The built-in All models group cannot be edited.">
          <AppTextInput v-model="name" :maxlength="80" placeholder="vippro" />
        </AppField>
        <AppField label="Description"><AppTextInput v-model="description" placeholder="Optional" /></AppField>
        <ModelPicker v-model="models" :catalog="catalog" :problems="aliasProblems" />
        <AppButton kind="primary" type="submit" :disabled="saving || !name.trim() || !models.length || aliasInvalid">{{ editing ? 'Save group' : 'Create group' }}</AppButton>
      </form>
    </AppSheet>

    <AppModal :open="Boolean(pendingDelete)" kicker="Confirm" title="Delete this group?" @close="pendingDelete = null">
      <p class="section-copy">“{{ pendingDelete?.name }}” will be removed. Keys that still use it block the delete.</p>
      <p v-if="deleteError" class="alert mt-3" role="alert">{{ deleteError }}</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton @click="pendingDelete = null">Cancel</AppButton>
        <AppButton kind="danger" @click="remove">Delete group</AppButton>
      </div>
    </AppModal>
  </div>
</template>
