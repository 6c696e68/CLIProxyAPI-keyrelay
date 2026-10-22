import { ttlSecondsFromLocal } from '@/lib/datetime'
import { bulkPayload } from '@/lib/bulk'
import type { AdminService, BulkPatch, CreateDraft, CreatedTunnel, EditDraft, GroupInput, KeysQuery, Policy, RequestQuery, Scenario, TunnelDraft, UsageQuery } from '@/types'
import { managementJSON, managementRequest } from './http'
import { mapDiscoveredModels, mapGroup, mapKey, mapPolicyModels, mapRequest, mapStats, mapSummary, mapTunnel, mapUsage } from './map'
import { asRecord, pickNumber, pickString } from './pick'

const ROOT = '/v0/management/plugins/key-shop'
const KEYS_PATH = `${ROOT}/keys`
const POLICY_PATH = '/v8/management/config/plugins/configs/key-shop'
const ACCESS_KEYS_PATH = '/v8/management/config/access/api-keys'
const PAGE_SIZE = 25

function mapCreatedTunnel(raw: unknown): CreatedTunnel | null {
  const row = asRecord(raw)
  if (!row || (!pickString(row, 'id') && !pickString(row, 'error') && !pickString(row, 'endpoint'))) return null
  return {
    id: pickString(row, 'id'),
    name: pickString(row, 'name'),
    status: pickString(row, 'status'),
    publicUrl: pickString(row, 'public_url'),
    endpoint: pickString(row, 'endpoint'),
    error: pickString(row, 'error'),
  }
}

function queryString(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value == null || value === '') continue
    search.set(key, String(value))
  }
  const text = search.toString()
  return text ? `?${text}` : ''
}

export function createLiveAdminService(): AdminService {
  let secret = ''
  let discovered: string[] | null = null
  let loadedConfig: Record<string, unknown> = {}
  let policyLoaded = false

  async function call(method: string, path: string, body?: unknown, timeoutMs?: number) {
    return managementRequest(secret, method, path, body, timeoutMs)
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
    if (discovered) return discovered
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
        if (models.length) {
          discovered = models
          return discovered
        }
      } catch {
        // try the next saved operator key
      }
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

  async function loadPolicy(): Promise<Policy> {
    try {
      const nextConfig = await call('GET', POLICY_PATH)
      loadedConfig = nextConfig
      policyLoaded = true
      return {
        mode: nextConfig.model_access_mode === 'custom' ? 'custom' : 'full',
        models: mapPolicyModels(nextConfig.models),
        loaded: true,
      }
    } catch {
      policyLoaded = false
      return { mode: 'full', models: [], loaded: false }
    }
  }

  return {
    pageSize: PAGE_SIZE,
    async authenticate(value: string) {
      const next = value.trim()
      if (!next) throw new Error('Enter the management key.')
      secret = next
      try {
        await call('GET', `${ROOT}/stats`)
      } catch (error) {
        secret = ''
        const message = error instanceof Error ? error.message : ''
        if (message.includes('did not respond')) throw error
        throw new Error('The management key could not be authenticated.')
      }
    },
    async stats(_scenario?: Scenario) {
      return mapStats(await call('GET', `${ROOT}/stats`))
    },
    async usage(query: UsageQuery, _scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/stats/usage${queryString({ from: query.from, to: query.to, bucket: query.bucket })}`)
      return mapUsage(data)
    },
    async policy(_scenario?: Scenario) {
      return loadPolicy()
    },
    async savePolicy(next: Policy, _scenario?: Scenario) {
      if (!policyLoaded) await loadPolicy()
      if (!policyLoaded) throw new Error('Model policy has not loaded.')
      const models = next.models.map((item) => ({
        name: item.name.trim(),
        ...(item.alias.trim() ? { alias: item.alias.trim() } : {}),
      })).filter((item) => item.name)
      const payload = { ...loadedConfig, model_access_mode: next.mode, models }
      await call('PUT', POLICY_PATH, payload)
      loadedConfig = payload
    },
    async groups(_scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/groups`)
      return Array.isArray(data.groups) ? data.groups.map(mapGroup) : []
    },
    async saveGroup(input: GroupInput, _scenario?: Scenario) {
      const body = {
        ...(input.id ? { id: input.id } : {}),
        name: input.name.trim(),
        description: input.description.trim(),
        all_models: input.allModels,
        models: input.models.map((item) => ({
          name: item.name.trim(),
          alias: item.alias.trim(),
        })).filter((item) => item.name),
      }
      const data = await call('POST', input.id ? `${ROOT}/groups/update` : `${ROOT}/groups`, body)
      return mapGroup(data.group)
    },
    async removeGroup(id: string, _scenario?: Scenario) {
      await call('POST', `${ROOT}/groups/delete`, { id })
    },
    async catalog(_scenario?: Scenario) {
      return catalogModels()
    },
    async keys(query: KeysQuery, _scenario?: Scenario) {
      const data = await call('GET', `${KEYS_PATH}${queryString({
        q: query.q,
        status: query.status,
        group: query.group,
        page: query.page,
        page_size: query.pageSize,
      })}`)
      const keys = Array.isArray(data.keys) ? data.keys.map(mapKey) : []
      return { keys, total: pickNumber(data, 'total') || keys.length }
    },
    async create(draft: CreateDraft, _scenario?: Scenario) {
      const count = Math.min(500, Math.max(1, Number(draft.count) || 1))
      const body: Record<string, unknown> = {
        label: draft.label,
        customer: draft.customer,
        note: draft.note,
        count,
        group_id: draft.groupId,
        ttl_seconds: draft.neverExpires ? 0 : ttlSecondsFromLocal(draft.expiresLocal),
        token_limit: Number(draft.tokenLimit),
        request_limit: Number(draft.requestLimit),
        never_expires: draft.neverExpires,
        unlimited_tokens: draft.unlimitedTokens,
        unlimited_requests: draft.unlimitedRequests,
        ...(draft.requestId ? { request_id: draft.requestId } : {}),
      }
      if (draft.tunnelEnabled) {
        body.tunnel = { enabled: true, ...(draft.tunnelTarget?.trim() ? { target_url: draft.tunnelTarget.trim() } : {}) }
      }
      const data = await call('POST', count === 1 ? KEYS_PATH : `${KEYS_PATH}/bulk`, body, 30000)
      const secrets = typeof data.secret === 'string' && data.secret
        ? [data.secret]
        : Array.isArray(data.keys) ? data.keys.map((item) => pickString(asRecord(item), 'Secret', 'secret')).filter(Boolean) : []
      return { secrets, tunnel: mapCreatedTunnel(data.tunnel) }
    },
    async update(draft: EditDraft, _scenario?: Scenario) {
      await call('POST', `${KEYS_PATH}/update`, {
        id: draft.id,
        label: draft.label,
        customer: draft.customer,
        group_id: draft.groupId,
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
    async bulkUpdate(ids: string[], patch: BulkPatch, _scenario?: Scenario) {
      const data = await call('POST', `${KEYS_PATH}/bulk-update`, bulkPayload(ids, patch))
      return pickNumber(data, 'updated')
    },
    async bulkDelete(ids: string[], _scenario?: Scenario) {
      const data = await call('POST', `${KEYS_PATH}/bulk-delete`, { ids })
      return pickNumber(data, 'deleted')
    },
    async requests(query: RequestQuery, _scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/requests${queryString({
        id: query.id,
        customer: query.customer,
        group: query.group,
        status: query.status,
        model: query.model,
        account: query.account,
        effort: query.effort,
        code: query.code,
        q: query.q,
        from: query.from,
        to: query.to,
        limit: query.limit,
        cursor: query.cursor,
      })}`)
      return {
        requests: Array.isArray(data.requests) ? data.requests.map(mapRequest) : [],
        nextCursor: pickString(data, 'next_cursor'),
        hasMore: Boolean(data.has_more),
        total: pickNumber(data, 'total'),
        summary: mapSummary(data.summary),
      }
    },
    async tunnels(_scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/tunnels`)
      const info = asRecord(data.cloudflared)
      return {
        tunnels: Array.isArray(data.tunnels) ? data.tunnels.map(mapTunnel) : [],
        cloudflared: {
          found: Boolean(info.found),
          path: pickString(info, 'path'),
          version: pickString(info, 'version'),
        },
      }
    },
    async saveTunnel(draft: TunnelDraft, _scenario?: Scenario) {
      const token = draft.token.trim()
      if (draft.id) {
        const body: Record<string, unknown> = {
          id: draft.id,
          name: draft.name.trim(),
          target_url: draft.targetUrl.trim(),
          autostart: draft.autostart,
        }
        if (token) body.token = token
        return mapTunnel(await call('POST', `${ROOT}/tunnels/update`, body))
      }
      return mapTunnel(await call('POST', `${ROOT}/tunnels`, {
        name: draft.name.trim(),
        kind: draft.kind,
        target_url: draft.targetUrl.trim(),
        token,
        autostart: draft.autostart,
      }))
    },
    async removeTunnel(id: string, _scenario?: Scenario) {
      await call('POST', `${ROOT}/tunnels/delete`, { id })
    },
    async startTunnel(id: string, _scenario?: Scenario) {
      return mapTunnel(await call('POST', `${ROOT}/tunnels/start`, { id }))
    },
    async stopTunnel(id: string, _scenario?: Scenario) {
      return mapTunnel(await call('POST', `${ROOT}/tunnels/stop`, { id }))
    },
    async tunnelLogs(id: string, lines = 200, _scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/tunnels/logs${queryString({ id, lines })}`)
      return Array.isArray(data.lines) ? data.lines.map((line) => String(line)) : []
    },
  }
}

export type LiveAdminService = AdminService
