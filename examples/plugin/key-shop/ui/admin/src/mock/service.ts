import { ApiError } from '@/lib/api-error'
import { groupModelProblems } from '@/lib/group-models'
import { bulkPayload } from '@/lib/bulk'
import { availableModels, seedGroups, seedKeys, seedPolicyModels, seedRequests } from '@/mock/fixtures'
import type { AdminService, BulkPatch, CreateDraft, EditDraft, GroupInput, KeyRecord, KeysQuery, ModelEntry, ModelGroup, Policy, RequestPage, RequestQuery, RequestRow, RequestSummary, Scenario, Stats, Tunnel, TunnelDraft, UsageQuery, UsageRank, UsageReport } from '@/types'

const PAGE_SIZE = 5

function clone<T>(value: T): T {
  return structuredClone(value)
}

function normalizeGroupModels(models: ModelEntry[]): ModelEntry[] {
  const out: ModelEntry[] = []
  const seen = new Set<string>()
  for (const item of models) {
    const name = item.name.trim()
    if (!name) continue
    const key = name.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    const alias = item.alias.trim()
    out.push({ name, alias: alias.toLowerCase() === key ? '' : alias })
  }
  if (Object.keys(groupModelProblems(out)).length) throw new Error('names and aliases must be unique within the group')
  return out
}

function displayFrom(secret: string): string {
  return `${secret.slice(0, 7)}…${secret.slice(-4)}`
}

function fakeSecret(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return `ck_${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
}

function assertReadable(scenario?: Scenario) {
  if (scenario === 'error') throw new Error('The operations API did not respond.')
}

function assertWritable(scenario?: Scenario) {
  if (scenario && scenario !== 'ready' && scenario !== 'limited') throw new Error('Changes cannot be saved in this preview state.')
}

function stampRecent(rows: RequestRow[]): RequestRow[] {
  const span = 6 * 86_400_000
  return rows.map((row, index) => {
    const at = new Date(Date.now() - (index / Math.max(1, rows.length - 1)) * span).toISOString()
    return { ...row, requestedAt: at, completedAt: row.status === 'pending' ? null : at }
  })
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

function summarize(rows: RequestRow[]): RequestSummary {
  const latency = rows.filter((row) => row.latencyMs)
  const ttft = rows.filter((row) => row.ttftMs)
  const succeeded = rows.filter((row) => row.status === 'succeeded').length
  const failed = rows.filter((row) => row.status === 'failed').length
  const done = succeeded + failed
  return {
    total: rows.length,
    succeeded,
    failed,
    canceled: rows.filter((row) => row.status === 'canceled').length,
    rejected: rows.filter((row) => row.status === 'rejected').length,
    input: rows.reduce((sum, row) => sum + row.input, 0),
    output: rows.reduce((sum, row) => sum + row.output, 0),
    reasoning: rows.reduce((sum, row) => sum + row.reasoning, 0),
    cached: rows.reduce((sum, row) => sum + row.cached, 0),
    billed: rows.reduce((sum, row) => sum + row.billed, 0),
    avgLatencyMs: latency.length ? latency.reduce((sum, row) => sum + (row.latencyMs || 0), 0) / latency.length : 0,
    avgTtftMs: ttft.length ? ttft.reduce((sum, row) => sum + (row.ttftMs || 0), 0) / ttft.length : 0,
    errorRate: done ? failed / done : 0,
  }
}

function blankKey(partial: Pick<KeyRecord, 'id' | 'label' | 'display' | 'customer' | 'groupId' | 'tokenLimit' | 'requestLimit' | 'expiresAt' | 'note'>): KeyRecord {
  return {
    ...partial,
    status: 'active',
    tokensUsed: 0,
    requestQuotaUsed: 0,
    requestCount: 0,
    requestSuccessCount: 0,
    requestFailureCount: 0,
    requestCanceledCount: 0,
    requestRejectedCount: 0,
    requestLimitReached: false,
    tokenLimitReached: false,
    createdAt: new Date().toISOString(),
    lastUsedAt: null,
  }
}

export function createAdminService(): AdminService {
  const keys = clone(seedKeys)
  const requests = stampRecent(clone(seedRequests))
  const groups = clone(seedGroups)
  const tunnels: Tunnel[] = [{
    id: 'tn_demo00000001',
    name: 'edge',
    kind: 'quick',
    targetUrl: 'http://127.0.0.1:8319',
    hasToken: false,
    tokenHint: '',
    autostart: false,
    desired: 'running',
    status: 'running',
    pid: 4242,
    publicUrl: 'https://demo.trycloudflare.com',
    lastError: '',
    restarts: 0,
    startedAt: '2026-10-20T00:00:00Z',
    createdAt: '2026-10-20T00:00:00Z',
    updatedAt: '2026-10-20T00:00:00Z',
  }]
  let policy: Policy = { mode: 'custom', models: clone(seedPolicyModels), loaded: true }

  function syncCounts() {
    for (const group of groups) group.keyCount = keys.filter((key) => key.groupId === group.id).length
  }
  function requireGroup(id: string) {
    if (!id.trim() || !groups.some((group) => group.id === id)) throw new Error('unknown model group')
  }
  function groupName(id: string) {
    return groups.find((group) => group.id === id)?.name || ''
  }
  syncCounts()

  function filteredRequests(query: RequestQuery): RequestRow[] {
    const needle = query.q.trim().toLowerCase()
    return requests.filter((row) => {
      if (query.id && row.keyId !== query.id) return false
      if (query.customer && !row.customer.toLowerCase().includes(query.customer.trim().toLowerCase())) return false
      if (query.group && row.groupId !== query.group) return false
      if (query.status && row.status !== query.status) return false
      if (query.model && row.model !== query.model && row.requestedModel !== query.model) return false
      if (query.account && !`${row.authId} ${row.authIndex}`.toLowerCase().includes(query.account.trim().toLowerCase())) return false
      if (query.effort && row.effort !== query.effort) return false
      if (query.code && row.rejectionCode !== query.code) return false
      if (query.from && new Date(row.requestedAt).getTime() < new Date(query.from).getTime()) return false
      if (query.to && new Date(row.requestedAt).getTime() > new Date(query.to).getTime()) return false
      if (!needle) return true
      return [row.requestId, row.model, row.requestedModel, row.keyLabel, row.customer, row.keyId].join(' ').toLowerCase().includes(needle)
    })
  }

  return {
    pageSize: PAGE_SIZE,
    async authenticate(secret: string) {
      if (!secret.trim()) throw new Error('Enter the management key.')
      if (secret.trim().toLowerCase() === 'bad') throw new Error('The management key could not be authenticated.')
    },
    async stats(scenario?: Scenario) {
      assertReadable(scenario)
      if (scenario === 'empty') return statsFor([], [])
      return statsFor(keys, requests)
    },
    async usage(query: UsageQuery, scenario?: Scenario): Promise<UsageReport> {
      assertReadable(scenario)
      if (scenario === 'empty') {
        return {
          from: query.from, to: query.to, bucket: query.bucket, summary: summarize([]), series: [],
          topModels: [], topKeys: [], topCustomers: [], topAccounts: [],
          latencyP50: 0, latencyP95: 0, ttftP50: 0, ttftP95: 0, cacheHitRatio: 0,
        }
      }
      const matched = filteredRequests({
        id: '', customer: '', group: '', status: '', model: '', account: '', effort: '', code: '', q: '',
        from: query.from, to: query.to, limit: 0, cursor: '',
      })
      const summary = summarize(matched)
      const width = query.bucket === 'hour' ? 13 : 10
      const buckets = new Map<string, RequestRow[]>()
      for (const row of matched) {
        const key = row.requestedAt.slice(0, width)
        buckets.set(key, [...(buckets.get(key) || []), row])
      }
      const series = [...buckets.entries()].sort(([left], [right]) => left.localeCompare(right)).map(([key, rows]) => ({
        start: query.bucket === 'hour' ? `${key}:00:00.000Z` : `${key}T00:00:00.000Z`,
        requests: rows.length,
        succeeded: rows.filter((row) => row.status === 'succeeded').length,
        failed: rows.filter((row) => row.status === 'failed').length,
        rejected: rows.filter((row) => row.status === 'rejected').length,
        canceled: rows.filter((row) => row.status === 'canceled').length,
        input: rows.reduce((sum, row) => sum + row.input, 0),
        output: rows.reduce((sum, row) => sum + row.output, 0),
        billed: rows.reduce((sum, row) => sum + row.billed, 0),
      }))
      const rank = (pick: (row: RequestRow) => { id: string; label: string; detail: string }): UsageRank[] => {
        const map = new Map<string, UsageRank>()
        for (const row of matched) {
          const item = pick(row)
          if (!item.id) continue
          const current = map.get(item.id) || { ...item, requests: 0, billed: 0 }
          current.requests += 1
          current.billed += row.billed
          map.set(item.id, current)
        }
        return [...map.values()].sort((left, right) => right.requests - left.requests).slice(0, 8)
      }
      return {
        from: query.from,
        to: query.to,
        bucket: query.bucket,
        summary,
        series,
        topModels: rank((row) => ({ id: row.model, label: row.model, detail: '' })),
        topKeys: rank((row) => ({ id: row.keyId, label: row.keyLabel || row.keyId, detail: row.customer })),
        topCustomers: rank((row) => ({ id: row.customer, label: row.customer, detail: '' })),
        topAccounts: rank((row) => ({ id: row.authId, label: row.authId, detail: row.provider })),
        latencyP50: 1800,
        latencyP95: 6120,
        ttftP50: 640,
        ttftP95: 1210,
        cacheHitRatio: summary.input ? summary.cached / summary.input : 0,
      }
    },
    async policy(scenario?: Scenario) {
      assertReadable(scenario)
      if (scenario === 'empty') return { mode: 'full', models: [], loaded: true }
      return clone(policy)
    },
    async savePolicy(next: Policy, scenario?: Scenario) {
      assertWritable(scenario)
      if (!next.loaded) throw new Error('Model policy has not loaded.')
      policy = clone(next)
    },
    async groups(scenario?: Scenario) {
      assertReadable(scenario)
      syncCounts()
      if (scenario === 'empty') return []
      return clone(groups)
    },
    async saveGroup(input: GroupInput, scenario?: Scenario) {
      assertWritable(scenario)
      const name = input.name.trim()
      if (!name || name.length > 80) throw new Error('name must be 1-80 characters')
      const models = normalizeGroupModels(input.models)
      if (!input.allModels && models.length < 1) throw new Error('choose at least one model')
      const clash = groups.find((group) => group.name.toLowerCase() === name.toLowerCase() && group.id !== input.id)
      if (clash) throw new Error('name already exists')
      if (input.id) {
        const group = groups.find((item) => item.id === input.id)
        if (!group) throw new Error('model group not found')
        if (group.isSystem) throw new Error('built-in model group cannot be changed')
        group.name = name
        group.description = input.description.trim()
        group.allModels = input.allModels
        group.models = input.allModels ? [] : models
        group.updatedAt = new Date().toISOString()
        return clone(group)
      }
      const group: ModelGroup = {
        id: `grp_${fakeSecret().slice(3, 11)}`,
        name,
        description: input.description.trim(),
        allModels: input.allModels,
        models: input.allModels ? [] : models,
        isSystem: false,
        keyCount: 0,
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
      }
      groups.push(group)
      return clone(group)
    },
    async removeGroup(id: string, scenario?: Scenario) {
      assertWritable(scenario)
      const index = groups.findIndex((group) => group.id === id)
      if (index < 0) throw new Error('model group not found')
      const group = groups[index]
      if (group.isSystem) throw new Error('built-in model group cannot be changed')
      const count = keys.filter((key) => key.groupId === id).length
      if (count) throw new ApiError(`model group is used by ${count} key(s)`, 409, count)
      groups.splice(index, 1)
    },
    async catalog(scenario?: Scenario) {
      assertReadable(scenario)
      return scenario === 'empty' ? [] : [...availableModels]
    },
    async keys(query: KeysQuery, scenario?: Scenario) {
      assertReadable(scenario)
      const source = scenario === 'empty' ? [] : scenario === 'limited'
        ? keys.map((key, index) => index < 3 ? { ...key, requestLimitReached: true, status: key.status === 'disabled' ? key.status : 'active' as const } : key)
        : keys
      const needle = query.q.trim().toLowerCase()
      const filtered = source.filter((key) => {
        const matchesQuery = !needle || [key.label, key.customer, key.id, key.display].join(' ').toLowerCase().includes(needle)
        const matchesStatus = !query.status || key.status === query.status
        const matchesGroup = !query.group || (query.group === 'none' ? !key.groupId : key.groupId === query.group)
        return matchesQuery && matchesStatus && matchesGroup
      })
      const size = query.pageSize || PAGE_SIZE
      return { keys: clone(filtered.slice((query.page - 1) * size, query.page * size)), total: filtered.length }
    },
    async create(draft: CreateDraft, scenario?: Scenario) {
      assertWritable(scenario)
      requireGroup(draft.groupId)
      const count = Math.min(500, Math.max(1, Number(draft.count) || 1))
      const secrets: string[] = []
      for (let index = 0; index < count; index += 1) {
        const secret = fakeSecret()
        const id = `key_${secret.slice(3, 11)}`
        const label = draft.label.trim() || `key-${id.slice(-8)}`
        keys.unshift(blankKey({
          id,
          label: count > 1 ? `${label}-${secret.slice(-4)}` : label,
          display: displayFrom(secret),
          customer: draft.customer.trim(),
          groupId: draft.groupId,
          tokenLimit: draft.unlimitedTokens ? 0 : Number(draft.tokenLimit) || 0,
          requestLimit: draft.unlimitedRequests ? 0 : Number(draft.requestLimit) || 0,
          expiresAt: draft.neverExpires ? null : new Date(draft.expiresLocal).toISOString(),
          note: draft.note.trim(),
        }))
        secrets.push(secret)
      }
      syncCounts()
      const tunnel = draft.tunnelEnabled
        ? { id: 'tn_mocktunnel01', name: 'key-mock', status: 'running', publicUrl: 'https://demo.trycloudflare.com', endpoint: 'https://demo.trycloudflare.com/v1', error: '' }
        : null
      return { secrets, tunnel }
    },
    async update(draft: EditDraft, scenario?: Scenario) {
      assertWritable(scenario)
      requireGroup(draft.groupId)
      const key = keys.find((item) => item.id === draft.id)
      if (!key) throw new Error('Key not found.')
      key.label = draft.label.trim() || key.label
      key.customer = draft.customer.trim()
      key.note = draft.note
      key.groupId = draft.groupId
      key.status = draft.status
      key.tokenLimit = Number(draft.tokenLimit) || 0
      key.requestLimit = draft.unlimitedRequests ? 0 : Number(draft.requestLimit) || 0
      key.expiresAt = draft.neverExpires ? null : new Date(draft.expiresLocal).toISOString()
      key.requestLimitReached = key.requestLimit > 0 && key.requestQuotaUsed >= key.requestLimit
      key.tokenLimitReached = key.tokenLimit > 0 && key.tokensUsed >= key.tokenLimit
      if (key.tokenLimitReached) key.status = 'exhausted'
      for (const row of requests) if (row.keyId === key.id) row.groupName = groupName(key.groupId)
      syncCounts()
    },
    async setEnabled(id: string, enabled: boolean, scenario?: Scenario) {
      assertWritable(scenario)
      const key = keys.find((item) => item.id === id)
      if (!key) throw new Error('Key not found.')
      key.status = enabled ? 'active' : 'disabled'
    },
    async rotate(id: string, scenario?: Scenario) {
      assertWritable(scenario)
      const key = keys.find((item) => item.id === id)
      if (!key) throw new Error('Key not found.')
      const secret = fakeSecret()
      key.display = displayFrom(secret)
      return secret
    },
    async resetUsage(id: string, scenario?: Scenario) {
      assertWritable(scenario)
      const key = keys.find((item) => item.id === id)
      if (!key) throw new Error('Key not found.')
      key.tokensUsed = 0
      key.tokenLimitReached = false
      if (key.status === 'exhausted') key.status = 'active'
    },
    async remove(id: string, scenario?: Scenario) {
      assertWritable(scenario)
      const index = keys.findIndex((item) => item.id === id)
      if (index < 0) throw new Error('Key not found.')
      keys.splice(index, 1)
      syncCounts()
    },
    async bulkUpdate(ids: string[], patch: BulkPatch, scenario?: Scenario) {
      assertWritable(scenario)
      const body = bulkPayload(ids, patch)
      if (Object.keys(body).length < 2) throw new Error('no changes requested')
      if (typeof body.group_id === 'string') requireGroup(body.group_id)
      const found = ids.map((id) => keys.find((key) => key.id === id))
      if (found.some((key) => !key)) throw new ApiError('one or more keys were not found', 404)
      for (const key of found) {
        if (!key) continue
        if (typeof body.group_id === 'string') key.groupId = body.group_id
        if (body.status === 'active' || body.status === 'disabled') key.status = body.status
        if (typeof body.customer === 'string') key.customer = body.customer
        if (typeof body.note === 'string') key.note = body.note
        if (body.never_expires === true) key.expiresAt = null
        else if (typeof body.ttl_seconds === 'number') key.expiresAt = new Date(Date.now() + body.ttl_seconds * 1000).toISOString()
        if (body.unlimited_tokens === true) key.tokenLimit = 0
        else if (typeof body.token_limit === 'number') key.tokenLimit = body.token_limit
        if (body.unlimited_requests === true) key.requestLimit = 0
        else if (typeof body.request_limit === 'number') key.requestLimit = body.request_limit
      }
      syncCounts()
      return ids.length
    },
    async bulkDelete(ids: string[], scenario?: Scenario) {
      assertWritable(scenario)
      if (!ids.length || ids.length > 500) throw new Error('select between 1 and 500 keys')
      if (ids.some((id) => !keys.some((key) => key.id === id))) throw new ApiError('one or more keys were not found', 404)
      for (const id of ids) {
        const index = keys.findIndex((key) => key.id === id)
        if (index >= 0) keys.splice(index, 1)
      }
      syncCounts()
      return ids.length
    },
    async requests(query: RequestQuery, scenario?: Scenario): Promise<RequestPage> {
      assertReadable(scenario)
      const matched = scenario === 'empty' ? [] : filteredRequests(query)
      const start = query.cursor ? Number(query.cursor) || 0 : 0
      const limit = query.limit || PAGE_SIZE
      const page = matched.slice(start, start + limit)
      const next = start + page.length
      return {
        requests: clone(page),
        nextCursor: next < matched.length ? String(next) : '',
        hasMore: next < matched.length,
        total: matched.length,
        summary: summarize(matched),
      }
    },
    async tunnels(scenario?: Scenario) {
      assertReadable(scenario)
      if (scenario === 'empty') return { tunnels: [], cloudflared: { found: false, path: '', version: '' } }
      return { tunnels: clone(tunnels), cloudflared: { found: true, path: '/usr/local/bin/cloudflared', version: '2026.1.0' } }
    },
    async saveTunnel(draft: TunnelDraft, scenario?: Scenario) {
      assertWritable(scenario)
      const name = draft.name.trim()
      if (!name || name.length > 64) throw new Error('invalid name')
      const clash = tunnels.find((item) => item.name.toLowerCase() === name.toLowerCase() && item.id !== draft.id)
      if (clash) throw new ApiError('duplicate name', 409)
      if (draft.id) {
        const row = tunnels.find((item) => item.id === draft.id)
        if (!row) throw new ApiError('unknown id', 404)
        row.name = name
        row.targetUrl = draft.targetUrl.trim()
        row.autostart = draft.autostart
        if (draft.token.trim()) {
          row.hasToken = true
          row.tokenHint = draft.token.trim().slice(-4)
        }
        row.updatedAt = new Date().toISOString()
        return clone(row)
      }
      if (tunnels.length >= 20) throw new ApiError('tunnel limit reached', 409)
      if (draft.kind === 'token' && draft.token.trim().length < 20) throw new Error('invalid token')
      if (draft.kind === 'quick' && !draft.targetUrl.trim()) throw new Error('invalid target_url')
      const row: Tunnel = {
        id: `tn_${fakeSecret().slice(3, 15)}`,
        name,
        kind: draft.kind,
        targetUrl: draft.targetUrl.trim(),
        hasToken: draft.kind === 'token',
        tokenHint: draft.kind === 'token' ? draft.token.trim().slice(-4) : '',
        autostart: draft.autostart,
        desired: 'stopped',
        status: 'stopped',
        pid: 0,
        publicUrl: '',
        lastError: '',
        restarts: 0,
        startedAt: null,
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
      }
      tunnels.unshift(row)
      return clone(row)
    },
    async removeTunnel(id: string, scenario?: Scenario) {
      assertWritable(scenario)
      const index = tunnels.findIndex((item) => item.id === id)
      if (index < 0) throw new ApiError('unknown id', 404)
      tunnels.splice(index, 1)
    },
    async startTunnel(id: string, scenario?: Scenario) {
      assertWritable(scenario)
      const row = tunnels.find((item) => item.id === id)
      if (!row) throw new ApiError('unknown id', 404)
      if (row.status === 'running') throw new ApiError('already running', 409)
      row.status = 'running'
      row.desired = 'running'
      row.pid = 4242
      row.startedAt = new Date().toISOString()
      if (row.kind === 'quick') row.publicUrl = 'https://demo.trycloudflare.com'
      return clone(row)
    },
    async stopTunnel(id: string, scenario?: Scenario) {
      assertWritable(scenario)
      const row = tunnels.find((item) => item.id === id)
      if (!row) throw new ApiError('unknown id', 404)
      row.status = 'stopped'
      row.desired = 'stopped'
      row.pid = 0
      return clone(row)
    },
    async tunnelLogs(id: string, lines = 200, scenario?: Scenario) {
      assertReadable(scenario)
      if (!tunnels.some((item) => item.id === id)) throw new ApiError('unknown id', 404)
      return ['cloudflared mock ready', 'https://demo.trycloudflare.com'].slice(0, lines)
    },
  }
}

export type { AdminService }
