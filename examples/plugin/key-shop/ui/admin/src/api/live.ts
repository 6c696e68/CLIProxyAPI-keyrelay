import { ttlSecondsFromLocal } from '@/lib/datetime'
import type { AdminQuery, AdminSnapshot, CreateDraft, EditDraft, Policy, Scenario } from '@/types'
import { ManagementAPIError, managementJSON, managementRequest } from './http'
import { mapDiscoveredModels, mapKey, mapPolicyModels, mapRequest, mapStats } from './map'
import { asRecord, pickNumber, pickString } from './pick'

const KEYS_PATH = '/v0/management/plugins/key-shop/keys'
const STATS_PATH = '/v0/management/plugins/key-shop/stats'
const REQUESTS_PATH = '/v0/management/plugins/key-shop/requests?limit=25'
const POLICY_PATH = '/v8/management/config/plugins/configs/key-shop'
const ACCESS_KEYS_PATH = '/v8/management/config/access/api-keys'
const PAGE_SIZE = 25

export function createLiveAdminService() {
  let secret = ''
  let discovered: string[] | null = null
  let loadedConfig: Record<string, unknown> = {}
  let policyLoaded = false
  const cursors: Record<string, string[]> = { '': [''] }

  async function call(method: string, path: string, body?: unknown) {
    return managementRequest(secret, method, path, body)
  }

  function savedAccessKeys(payload: unknown): string[] {
    if (!Array.isArray(payload)) return []
    const seen = new Set<string>()
    const keys: string[] = []
    for (const item of payload) {
      const record = asRecord(item)
      const value = typeof item === 'string' ? item : pickString(record, 'api-key', 'apiKey', 'key', 'Key')
      const key = value.trim()
      if (!key || seen.has(key)) continue
      seen.add(key)
      keys.push(key)
    }
    return keys
  }

  async function catalogModels(): Promise<string[]> {
    let keys: string[] = []
    try {
      keys = savedAccessKeys(await managementJSON(secret, 'GET', ACCESS_KEYS_PATH))
    } catch {
      keys = []
    }
    for (const key of keys) {
      try {
        const response = await fetch('/v1/models', {
          headers: { Authorization: `Bearer ${key}` },
          signal: AbortSignal.timeout(8000),
        })
        if (!response.ok) continue
        const models = mapDiscoveredModels(await response.json())
        if (models.length) return models
      } catch {
        // try the next saved operator key
      }
    }
    return []
  }

  async function discoverModels(): Promise<string[]> {
    if (discovered) return discovered
    const catalog = await catalogModels()
    if (catalog.length) {
      discovered = catalog
      return discovered
    }
    const fallback = ['codex', 'gemini', 'claude', 'xai', 'meta', 'gemini-interactions']
    const responses = await Promise.all(fallback.map(async (channel) => {
      try {
        return await call('GET', `/v8/management/routing/model-definitions/${channel}`)
      } catch {
        return null
      }
    }))
    const seen = new Set<string>()
    discovered = responses.flatMap((item) => mapDiscoveredModels(item)).filter((name) => {
      const key = name.toLowerCase()
      if (!key || seen.has(key)) return false
      seen.add(key)
      return true
    })
    return discovered
  }

  async function loadPolicy(): Promise<{ policy: Policy; available: string[] }> {
    try {
      const [nextConfig, models] = await Promise.all([
        call('GET', POLICY_PATH),
        discoverModels(),
      ])
      loadedConfig = nextConfig
      policyLoaded = true
      return {
        policy: {
          mode: nextConfig.model_access_mode === 'custom' ? 'custom' : 'full',
          models: mapPolicyModels(nextConfig.models),
          loaded: true,
        },
        available: models,
      }
    } catch {
      policyLoaded = false
      return { policy: { mode: 'full', models: [], loaded: false }, available: discovered || [] }
    }
  }

  async function loadRequests(selectedId: string, page: number) {
    const key = selectedId || ''
    const list = cursors[key] || ['']
    cursors[key] = list
    const cursor = page <= 1 ? '' : list[page - 1] || ''
    if (page > 1 && !cursor) return { requests: [], requestTotal: 0, requestHasMore: false }
    let path = REQUESTS_PATH
    if (key) path += `&id=${encodeURIComponent(key)}`
    if (cursor) path += `&cursor=${encodeURIComponent(cursor)}`
    const data = await call('GET', path)
    const next = pickString(data, 'next_cursor')
    if (next) list[page] = next
    const rows = Array.isArray(data.requests) ? data.requests.map(mapRequest) : []
    return {
      requests: rows,
      requestTotal: pickNumber(data, 'total'),
      requestHasMore: Boolean(data.has_more),
    }
  }

  return {
    pageSize: PAGE_SIZE,
    async authenticate(value: string) {
      secret = ''
      const next = value.trim()
      if (!next) throw new Error('Enter the management key.')
      if (next.startsWith('ck_')) {
        throw new Error('This is a customer API key (ck_...). Use management_password from ACCESS.txt to open the admin workspace.')
      }
      secret = next
      try {
        await call('GET', STATS_PATH)
      } catch (error) {
        secret = ''
        if (error instanceof ManagementAPIError && error.status === 401) {
          throw new Error('The management password is incorrect. Use management_password from ACCESS.txt, not a customer API key.')
        }
        throw error
      }
    },
    async load(input: AdminQuery): Promise<AdminSnapshot> {
      const listPath = `${KEYS_PATH}?q=${encodeURIComponent(input.query)}&status=${encodeURIComponent(input.status)}&page=${input.page}&page_size=25`
      const [list, summary, policy] = await Promise.all([
        call('GET', listPath),
        call('GET', STATS_PATH),
        loadPolicy(),
      ])
      const logs = await loadRequests(input.selectedId, input.requestPage)
      const keys = Array.isArray(list.keys) ? list.keys.map(mapKey) : []
      return {
        stats: mapStats(summary),
        keys,
        keysTotal: pickNumber(list, 'total') || keys.length,
        requests: logs.requests,
        requestTotal: logs.requestTotal,
        requestHasMore: logs.requestHasMore,
        policy: policy.policy,
        availableModels: policy.available,
      }
    },
    async savePolicy(next: Policy, _scenario?: Scenario) {
      if (!policyLoaded) throw new Error('Model policy has not loaded.')
      const models = next.models.map((item) => ({
        name: item.name.trim(),
        ...(item.alias.trim() ? { alias: item.alias.trim() } : {}),
      })).filter((item) => item.name)
      const payload = { ...loadedConfig, model_access_mode: next.mode, models }
      await call('PUT', POLICY_PATH, payload)
      loadedConfig = payload
    },
    async create(draft: CreateDraft, _scenario?: Scenario): Promise<string[]> {
      const count = Math.min(500, Math.max(1, Number(draft.count) || 1))
      const body = {
        label: draft.label,
        customer: draft.customer,
        note: draft.note,
        count,
        ttl_seconds: draft.neverExpires ? 0 : ttlSecondsFromLocal(draft.expiresLocal),
        token_limit: Number(draft.tokenLimit),
        request_limit: Number(draft.requestLimit),
        never_expires: draft.neverExpires,
        unlimited_tokens: draft.unlimitedTokens,
        unlimited_requests: draft.unlimitedRequests,
      }
      const data = await call('POST', count === 1 ? KEYS_PATH : `${KEYS_PATH}/bulk`, body)
      if (typeof data.secret === 'string' && data.secret) return [data.secret]
      return Array.isArray(data.keys) ? data.keys.map((item) => pickString(asRecord(item), 'Secret', 'secret')).filter(Boolean) : []
    },
    async update(draft: EditDraft, _scenario?: Scenario) {
      await call('POST', `${KEYS_PATH}/update`, {
        id: draft.id,
        label: draft.label,
        customer: draft.customer,
        token_limit: Number(draft.tokenLimit),
        request_limit: draft.unlimitedRequests ? 0 : Number(draft.requestLimit),
        unlimited_requests: draft.unlimitedRequests,
        status: draft.status,
        ...(draft.neverExpires ? { never_expires: true } : { ttl_seconds: ttlSecondsFromLocal(draft.expiresLocal) }),
      })
    },
    async setEnabled(id: string, enabled: boolean, _scenario?: Scenario) {
      await call('POST', `${KEYS_PATH}/update`, { id, status: enabled ? 'active' : 'disabled' })
    },
    async rotate(id: string, _scenario?: Scenario): Promise<string> {
      const data = await call('POST', `${KEYS_PATH}/rotate`, { id })
      return pickString(data, 'secret', 'Secret')
    },
    async resetUsage(id: string, _scenario?: Scenario) {
      await call('POST', `${KEYS_PATH}/reset-usage`, { id })
    },
    async remove(id: string, _scenario?: Scenario) {
      await call('POST', `${KEYS_PATH}/delete`, { id })
    },
  }
}

export type LiveAdminService = ReturnType<typeof createLiveAdminService>
