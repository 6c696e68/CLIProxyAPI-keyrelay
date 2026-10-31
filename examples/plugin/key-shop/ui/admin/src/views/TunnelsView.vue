<script setup lang="ts">
import { inject, onBeforeUnmount, ref, watch } from 'vue'
import PageHeader from '@/components/PageHeader.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppField from '@/components/ui/AppField.vue'
import AppModal from '@/components/ui/AppModal.vue'
import AppSheet from '@/components/ui/AppSheet.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import { useLoadGuard } from '@/composables/useLoadGuard'
import { useSubmitGuard } from '@/composables/useSubmitGuard'
import { bindViewBusy, notifyKey, refreshKey } from '@/composables/useToast'
import type { AdminService, CloudflaredInfo, Scenario, Tunnel, TunnelKind } from '@/types'

const props = withDefaults(defineProps<{ service: AdminService; scenario?: Scenario }>(), { scenario: 'ready' })
const notify = inject(notifyKey, () => {})
const refreshTick = inject(refreshKey, ref(0))
const tunnels = ref<Tunnel[]>([])
const cloudflared = ref<CloudflaredInfo>({ found: false, path: '', version: '' })
const error = ref('')
const { loading, load: runLoad } = useLoadGuard()
const editorOpen = ref(false)
const editing = ref<Tunnel | null>(null)
const name = ref('')
const kind = ref<TunnelKind>('quick')
const targetUrl = ref('')
const defaultTargetUrl = ref('')
const token = ref('')
const autostart = ref(false)
const { pending: busy, guard } = useSubmitGuard()
bindViewBusy(() => loading.value || busy.value)
const copying = ref(false)
const pendingDelete = ref<Tunnel | null>(null)
const deleteError = ref('')
const logsFor = ref<Tunnel | null>(null)
const logLines = ref<string[]>([])
const logError = ref('')
const logsLoading = ref(false)
let logTimer = 0
let logPull: Promise<void> | null = null
let refreshTimer = 0
let disposed = false

function statusClass(status: string) {
  if (status === 'running') return 'chip-good'
  if (status === 'starting') return 'chip-warn'
  if (status === 'failed') return 'chip-bad'
  return ''
}

function load() {
  return runLoad(async () => {
  if (props.scenario === 'loading') {
    tunnels.value = []
    return
  }
  error.value = ''
  try {
    const page = await props.service.tunnels(props.scenario)
    tunnels.value = page.tunnels
    cloudflared.value = page.cloudflared
    defaultTargetUrl.value = page.defaultTargetUrl
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Request failed'
  }
  }).then(syncRefresh)
}

// needsSettle marks a row whose status or public URL is still moving.
function needsSettle(row: Tunnel) {
  if (row.status === 'starting') return true
  if (row.status === 'running' && row.kind === 'quick' && !row.publicUrl) return true
  return row.status === 'failed' && row.desired === 'running'
}

// syncRefresh keeps one poll running only while some row is still settling.
function syncRefresh() {
  if (disposed) return
  const pending = tunnels.value.some(needsSettle)
  if (pending && !refreshTimer) {
    refreshTimer = window.setInterval(() => {
      if (loading.value) return
      void load()
    }, 2000)
    return
  }
  if (!pending && refreshTimer) {
    window.clearInterval(refreshTimer)
    refreshTimer = 0
  }
}

function targetPlaceholder() {
  return defaultTargetUrl.value || 'http://127.0.0.1:<CPA port>'
}

function openCreate() {
  if (busy.value) return
  editing.value = null
  name.value = ''
  kind.value = 'quick'
  targetUrl.value = defaultTargetUrl.value
  token.value = ''
  autostart.value = false
  editorOpen.value = true
}

function openEdit(row: Tunnel) {
  if (busy.value) return
  editing.value = row
  name.value = row.name
  kind.value = row.kind
  targetUrl.value = row.targetUrl
  token.value = ''
  autostart.value = row.autostart
  editorOpen.value = true
}

async function save() {
  if (busy.value) return
  await guard(async () => {
  try {
    const saved = await props.service.saveTunnel({
      id: editing.value?.id,
      name: name.value,
      kind: kind.value,
      targetUrl: targetUrl.value,
      token: token.value,
      autostart: autostart.value,
    }, props.scenario)
    editorOpen.value = false
    token.value = ''
    if (editing.value) notify('Tunnel saved')
    else if (saved.startError) notify(`Tunnel created, start failed: ${saved.startError}`)
    else notify('Tunnel created and starting')
    await load()
  } catch (err) {
    notify(err instanceof Error ? err.message : 'Tunnel could not be saved')
  }
  })
}

async function remove() {
  const target = pendingDelete.value
  if (!target || busy.value) return
  deleteError.value = ''
  await guard(async () => {
    try {
      await props.service.removeTunnel(target.id, props.scenario)
      notify('Tunnel deleted')
      pendingDelete.value = null
      await load()
    } catch (err) {
      deleteError.value = err instanceof Error ? err.message : 'Tunnel could not be deleted'
    }
  })
}

async function toggle(row: Tunnel) {
  if (busy.value) return
  await guard(async () => {
    try {
      if (row.status === 'running' || row.status === 'starting') await props.service.stopTunnel(row.id, props.scenario)
      else await props.service.startTunnel(row.id, props.scenario)
      await load()
    } catch (err) {
      notify(err instanceof Error ? err.message : 'Tunnel could not change state')
    }
  })
}

async function copyPublic(url: string) {
  if (copying.value) return
  copying.value = true
  try {
    await navigator.clipboard.writeText(url)
    notify('Public URL copied')
  } catch {
    notify('Could not copy the public URL')
  } finally {
    copying.value = false
  }
}

function pullLogs() {
  if (!logsFor.value) return Promise.resolve()
  if (logPull) return logPull
  const id = logsFor.value.id
  logsLoading.value = true
  logPull = (async () => {
    try {
      logLines.value = await props.service.tunnelLogs(id, 200, props.scenario)
      logError.value = ''
    } catch (err) {
      logError.value = err instanceof Error ? err.message : 'Logs failed'
    } finally {
      logsLoading.value = false
      logPull = null
    }
  })()
  return logPull
}

function openLogs(row: Tunnel) {
  if (logsLoading.value) return
  logsFor.value = row
  logLines.value = []
  logError.value = ''
  void pullLogs()
  window.clearInterval(logTimer)
  logTimer = window.setInterval(() => { void pullLogs() }, 2000)
}

function closeLogs() {
  logsFor.value = null
  window.clearInterval(logTimer)
  logTimer = 0
}

watch(() => props.scenario, () => { void load() }, { immediate: true })
watch(refreshTick, () => { void load() })
onBeforeUnmount(() => {
  disposed = true
  window.clearInterval(logTimer)
  window.clearInterval(refreshTimer)
  refreshTimer = 0
})
</script>

<template>
  <div>
    <PageHeader kicker="Activity" title="Tunnels" copy="Cloudflare tunnels this plugin created. Device tunnels are left alone.">
      <AppButton kind="primary" :disabled="busy" @click="openCreate">{{ busy ? 'Working...' : 'New tunnel' }}</AppButton>
    </PageHeader>
    <p v-if="!loading && !cloudflared.found" class="alert mb-4" role="status">cloudflared was not found. Start will fail until cloudflared_path points at the binary or cloudflared is on PATH.</p>
    <p v-else-if="cloudflared.found" class="notice mb-4" role="status">cloudflared {{ cloudflared.version || 'installed' }} at {{ cloudflared.path }}</p>
    <p v-if="error" class="alert mb-4" role="alert">{{ error }} <button class="btn btn-sm btn-danger ml-2" type="button" :disabled="loading" @click="load">{{ loading ? 'Loading...' : 'Retry' }}</button></p>
    <section class="panel">
      <p v-if="loading && !tunnels.length" role="status">Loading tunnels…</p>
      <template v-else>
        <div class="table-shell only-desktop">
          <div class="table-scroll">
            <table class="data-table fit">
              <caption class="sr-only">Tunnels</caption>
              <thead><tr><th>Name</th><th>Status</th><th>Public URL</th><th class="actions">Actions</th></tr></thead>
              <tbody>
                <tr v-for="row in tunnels" :key="row.id">
                  <td>
                    <strong>{{ row.name }}</strong>
                    <div class="sub">{{ row.kind }} · {{ row.targetUrl || row.id }}<span v-if="row.hasToken"> · token …{{ row.tokenHint }}</span></div>
                    <div v-if="row.lastError" class="sub">{{ row.lastError }}</div>
                  </td>
                  <td><span class="chip" :class="statusClass(row.status)">{{ row.status }}</span></td>
                  <td>
                    <div v-if="row.kind === 'quick' && row.publicUrl" class="flex items-center gap-2">
                      <span class="truncate">{{ row.publicUrl }}</span>
                      <AppButton small :disabled="copying" @click="copyPublic(row.publicUrl)">{{ copying ? 'Copying...' : 'Copy' }}</AppButton>
                    </div>
                    <span v-else class="sub">—</span>
                  </td>
                  <td class="actions">
                    <div class="flex gap-1">
                      <AppButton small :disabled="busy" @click="toggle(row)">{{ busy ? 'Working...' : (row.status === 'running' || row.status === 'starting' ? 'Stop' : 'Start') }}</AppButton>
                      <AppButton small :disabled="logsLoading" @click="openLogs(row)">{{ logsLoading ? 'Loading...' : 'Logs' }}</AppButton>
                      <AppButton small :disabled="busy" @click="openEdit(row)">Edit</AppButton>
                      <AppButton small kind="danger" :disabled="busy" @click="busy ? undefined : (pendingDelete = row, deleteError = '')">Delete</AppButton>
                    </div>
                  </td>
                </tr>
                <tr v-if="!tunnels.length"><td colspan="4" class="py-10 text-center text-[var(--ink-soft)]">No tunnels yet.</td></tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="card-list only-mobile">
          <article v-for="row in tunnels" :key="row.id" class="record">
            <strong>{{ row.name }}</strong>
            <p class="sub">{{ row.kind }} · <span class="chip" :class="statusClass(row.status)">{{ row.status }}</span></p>
            <p v-if="row.kind === 'quick' && row.publicUrl" class="sub">{{ row.publicUrl }}</p>
            <div class="mt-3 flex flex-wrap gap-2">
              <AppButton small :disabled="busy" @click="toggle(row)">{{ busy ? 'Working...' : (row.status === 'running' || row.status === 'starting' ? 'Stop' : 'Start') }}</AppButton>
              <AppButton v-if="row.kind === 'quick' && row.publicUrl" small :disabled="copying" @click="copyPublic(row.publicUrl)">{{ copying ? 'Copying...' : 'Copy' }}</AppButton>
              <AppButton small :disabled="logsLoading" @click="openLogs(row)">{{ logsLoading ? 'Loading...' : 'Logs' }}</AppButton>
              <AppButton small :disabled="busy" @click="openEdit(row)">Edit</AppButton>
              <AppButton small kind="danger" :disabled="busy" @click="busy ? undefined : (pendingDelete = row, deleteError = '')">Delete</AppButton>
            </div>
          </article>
          <p v-if="!tunnels.length" class="record text-center text-sm text-[var(--ink-soft)]">No tunnels yet.</p>
        </div>
      </template>
    </section>

    <AppSheet :open="editorOpen" :title="editing ? 'Edit tunnel' : 'New tunnel'" :locked="busy" @close="editorOpen = false">
      <form class="grid gap-3" @submit.prevent="save">
        <AppField label="Name" hint="1–64 characters.">
          <AppTextInput v-model="name" :maxlength="64" placeholder="edge" />
        </AppField>
        <AppField label="Kind" hint="Kind cannot change after create.">
          <select v-model="kind" class="field" :disabled="Boolean(editing)">
            <option value="quick">quick</option>
            <option value="token">token</option>
          </select>
        </AppField>
        <AppField label="Target URL" :hint="kind === 'quick' ? 'Required. http or https.' : 'Optional hint.'">
          <AppTextInput v-model="targetUrl" :placeholder="targetPlaceholder()" />
        </AppField>
        <AppField v-if="kind === 'token'" label="Token" :hint="editing?.hasToken ? `Saved token ends in ${editing.tokenHint}. Leave blank to keep it.` : 'Write-only. 20 or more characters, no spaces.'">
          <AppTextInput v-model="token" type="password" autocomplete="off" placeholder="Paste a new token" />
        </AppField>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="autostart" type="checkbox" />
          Start when the plugin starts
        </label>
        <AppButton kind="primary" type="submit" :disabled="busy || !name.trim()">{{ busy ? 'Saving...' : (editing ? 'Save tunnel' : 'Create tunnel') }}</AppButton>
      </form>
    </AppSheet>

    <AppModal :open="Boolean(pendingDelete)" kicker="Confirm" title="Delete this tunnel?" :locked="busy" @close="pendingDelete = null">
      <p class="section-copy">“{{ pendingDelete?.name }}” stops first, then its plugin home directory and row are removed.</p>
      <p v-if="deleteError" class="alert mt-3" role="alert">{{ deleteError }}</p>
      <div class="mt-5 flex justify-end gap-2">
        <AppButton :disabled="busy" @click="pendingDelete = null">Cancel</AppButton>
        <AppButton kind="danger" :disabled="busy" @click="remove">{{ busy ? 'Deleting...' : 'Delete tunnel' }}</AppButton>
      </div>
    </AppModal>

    <AppSheet :open="Boolean(logsFor)" :title="logsFor ? `${logsFor.name} logs` : 'Logs'" @close="closeLogs">
      <p v-if="logError" class="alert mb-3" role="alert">{{ logError }}</p>
      <pre class="panel overflow-auto text-xs" style="max-height: 24rem; white-space: pre-wrap">{{ logLines.join('\n') || 'No log lines yet.' }}</pre>
    </AppSheet>
  </div>
</template>
