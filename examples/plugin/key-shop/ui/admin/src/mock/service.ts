import { toLocalInput } from '@/lib/datetime'
import { availableModels, seedKeys, seedPolicyModels, seedRequests } from '@/mock/fixtures'
import type { AdminQuery, AdminSnapshot, CreateDraft, EditDraft, KeyRecord, Policy, RequestRow, Scenario, Stats } from '@/types'

const PAGE_SIZE = 5

function clone<T>(value: T): T {
  return structuredClone(value)
}

function displayFrom(secret: string): string {
  return `${secret.slice(0, 7)}…${secret.slice(-4)}`
}

function fakeSecret(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return `ck_${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
}

function statsFor(keys: KeyRecord[], requests: RequestRow[]): Stats {
  const soon = Date.now() + 7 * 86_400_000
  return {
    keysTotal: keys.length,
    keysActive: keys.filter((key) => key.status === 'active').length,
    keysRequestLimited: keys.filter((key) => key.requestLimitReached).length,
    tokensUsedTotal: keys.reduce((sum, key) => sum + key.tokensUsed, 0),
    requestsTotal: requests.length,
    requestsAdmitted: requests.filter((row) => row.status !== 'rejected').length,
    requestsSucceeded: requests.filter((row) => row.status === 'succeeded').length,
    requestsFailed: requests.filter((row) => row.status === 'failed').length,
    requestsCanceled: requests.filter((row) => row.status === 'canceled').length,
    requestsRejected: requests.filter((row) => row.status === 'rejected').length,
    expiringWithin7d: keys.filter((key) => key.expiresAt && new Date(key.expiresAt).getTime() < soon && new Date(key.expiresAt).getTime() > Date.now()).length,
  }
}

function snapshot(keys: KeyRecord[], requests: RequestRow[], policy: Policy, query: AdminQuery): AdminSnapshot {
  const needle = query.query.trim().toLowerCase()
  const filtered = keys.filter((key) => {
    const matchesQuery = !needle || [key.label, key.customer, key.id, key.display].join(' ').toLowerCase().includes(needle)
    return matchesQuery && (!query.status || key.status === query.status)
  })
  const pagedKeys = filtered.slice((query.page - 1) * PAGE_SIZE, query.page * PAGE_SIZE)
  const filteredRequests = requests.filter((row) => !query.selectedId || row.keyId === query.selectedId)
  const pagedRequests = filteredRequests.slice((query.requestPage - 1) * PAGE_SIZE, query.requestPage * PAGE_SIZE)
  return {
    stats: statsFor(keys, requests),
    keys: clone(pagedKeys),
    keysTotal: filtered.length,
    requests: clone(pagedRequests),
    requestTotal: filteredRequests.length,
    requestHasMore: query.requestPage * PAGE_SIZE < filteredRequests.length,
    policy: clone(policy),
    availableModels: [...availableModels],
  }
}

export function createAdminService() {
  const keys = clone(seedKeys)
  const requests = clone(seedRequests)
  let policy: Policy = { mode: 'custom', models: clone(seedPolicyModels), loaded: true }

  return {
    pageSize: PAGE_SIZE,
    async authenticate(secret: string) {
      if (!secret.trim()) throw new Error('Enter the management key.')
      if (secret.trim().toLowerCase() === 'bad') throw new Error('The management key could not be authenticated.')
    },
    async load(input: AdminQuery): Promise<AdminSnapshot> {
      if (input.scenario === 'error') throw new Error('The operations API did not respond.')
      if (input.scenario === 'empty') {
        return snapshot([], [], { mode: 'full', models: [], loaded: true }, input)
      }
      if (input.scenario === 'limited') {
        const next = clone(keys).map((key, index) => index < 3 ? { ...key, requestLimitReached: true, status: key.status === 'disabled' ? key.status : 'active' as const } : key)
        return snapshot(next, requests, policy, input)
      }
      return snapshot(keys, requests, policy, input)
    },
    async savePolicy(next: Policy, scenario: Scenario) {
      if (scenario !== 'ready' && scenario !== 'limited') throw new Error('Policy cannot be saved in this preview state.')
      if (!next.loaded) throw new Error('Model policy has not loaded.')
      policy = clone(next)
    },
    async create(draft: CreateDraft, scenario: Scenario): Promise<string[]> {
      if (scenario !== 'ready' && scenario !== 'limited') throw new Error('Keys cannot be created in this preview state.')
      const count = Math.min(500, Math.max(1, Number(draft.count) || 1))
      const secrets: string[] = []
      for (let index = 0; index < count; index += 1) {
        const secret = fakeSecret()
        const id = `key_${secret.slice(3, 11)}`
        const label = draft.label.trim() || `key-${id.slice(-8)}`
        keys.unshift({
          id,
          label: count > 1 ? `${label}-${secret.slice(-4)}` : label,
          display: displayFrom(secret),
          customer: draft.customer.trim(),
          status: 'active',
          tokensUsed: 0,
          tokenLimit: draft.unlimitedTokens ? 0 : Number(draft.tokenLimit) || 0,
          requestQuotaUsed: 0,
          requestLimit: draft.unlimitedRequests ? 0 : Number(draft.requestLimit) || 0,
          requestCount: 0,
          requestSuccessCount: 0,
          requestFailureCount: 0,
          requestCanceledCount: 0,
          requestRejectedCount: 0,
          requestLimitReached: false,
          tokenLimitReached: false,
          createdAt: new Date().toISOString(),
          expiresAt: draft.neverExpires ? null : new Date(draft.expiresLocal).toISOString(),
          lastUsedAt: null,
          note: draft.note.trim(),
        })
        secrets.push(secret)
      }
      return secrets
    },
    async update(draft: EditDraft, scenario: Scenario) {
      if (scenario !== 'ready' && scenario !== 'limited') throw new Error('Changes cannot be saved in this preview state.')
      const key = keys.find((item) => item.id === draft.id)
      if (!key) throw new Error('Key not found.')
      key.label = draft.label.trim() || key.label
      key.customer = draft.customer.trim()
      key.note = draft.note
      key.status = draft.status
      key.tokenLimit = Number(draft.tokenLimit) || 0
      key.requestLimit = draft.unlimitedRequests ? 0 : Number(draft.requestLimit) || 0
      key.expiresAt = draft.neverExpires ? null : new Date(draft.expiresLocal).toISOString()
      key.requestLimitReached = key.requestLimit > 0 && key.requestQuotaUsed >= key.requestLimit
      key.tokenLimitReached = key.tokenLimit > 0 && key.tokensUsed >= key.tokenLimit
      if (key.tokenLimitReached) key.status = 'exhausted'
    },
    async setEnabled(id: string, enabled: boolean, scenario: Scenario) {
      if (scenario !== 'ready' && scenario !== 'limited') throw new Error('Status cannot change in this preview state.')
      const key = keys.find((item) => item.id === id)
      if (!key) throw new Error('Key not found.')
      key.status = enabled ? 'active' : 'disabled'
    },
    async rotate(id: string, scenario: Scenario): Promise<string> {
      if (scenario !== 'ready' && scenario !== 'limited') throw new Error('Keys cannot be rotated in this preview state.')
      const key = keys.find((item) => item.id === id)
      if (!key) throw new Error('Key not found.')
      const secret = fakeSecret()
      key.display = displayFrom(secret)
      return secret
    },
    async resetUsage(id: string, scenario: Scenario) {
      if (scenario !== 'ready' && scenario !== 'limited') throw new Error('Usage cannot be reset in this preview state.')
      const key = keys.find((item) => item.id === id)
      if (!key) throw new Error('Key not found.')
      key.tokensUsed = 0
      key.tokenLimitReached = false
      if (key.status === 'exhausted') key.status = 'active'
    },
    async remove(id: string, scenario: Scenario) {
      if (scenario !== 'ready' && scenario !== 'limited') throw new Error('Keys cannot be deleted in this preview state.')
      const index = keys.findIndex((item) => item.id === id)
      if (index < 0) throw new Error('Key not found.')
      keys.splice(index, 1)
    },
  }
}

export function defaultCreateDraft(): CreateDraft {
  return {
    label: '',
    customer: '',
    note: '',
    count: 1,
    expiresLocal: toLocalInput(new Date(Date.now() + 7 * 86_400_000)),
    neverExpires: false,
    tokenLimit: 100_000_000,
    unlimitedTokens: false,
    requestLimit: 0,
    unlimitedRequests: true,
  }
}

export type AdminService = ReturnType<typeof createAdminService>
