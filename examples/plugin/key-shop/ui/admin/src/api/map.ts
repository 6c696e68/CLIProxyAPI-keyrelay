import type { KeyRecord, ModelEntry, ModelGroup, RequestRow, RequestSummary, Stats, UsagePoint, UsageRank, UsageReport } from '@/types'
import { asRecord, pick, pickNumber, pickString, pickTime } from './pick'

function pickOptionalNumber(source: Record<string, unknown>, ...keys: string[]): number | null {
  for (const key of keys) {
    if (!(key in source) || source[key] == null || source[key] === '') continue
    const value = Number(source[key])
    return Number.isFinite(value) ? value : null
  }
  return null
}

export function mapKey(raw: unknown): KeyRecord {
  const row = asRecord(raw)
  return {
    id: pickString(row, 'ID', 'id'),
    label: pickString(row, 'Label', 'label'),
    display: pickString(row, 'Display', 'display'),
    customer: pickString(row, 'Customer', 'customer'),
    groupId: pickString(row, 'GroupID', 'group_id'),
    status: (pickString(row, 'Status', 'status') || 'active') as KeyRecord['status'],
    tokensUsed: pickNumber(row, 'TokensUsed', 'tokens_used'),
    tokenLimit: pickNumber(row, 'TokenLimit', 'token_limit'),
    requestQuotaUsed: pickNumber(row, 'RequestQuotaUsed', 'request_quota_used'),
    requestLimit: pickNumber(row, 'RequestLimit', 'request_limit'),
    requestCount: pickNumber(row, 'RequestCount', 'request_count'),
    requestSuccessCount: pickNumber(row, 'RequestSuccessCount', 'request_success_count'),
    requestFailureCount: pickNumber(row, 'RequestFailureCount', 'request_failure_count'),
    requestCanceledCount: pickNumber(row, 'RequestCanceledCount', 'request_canceled_count'),
    requestRejectedCount: pickNumber(row, 'RequestRejectedCount', 'request_rejected_count'),
    requestLimitReached: Boolean(pick(row, 'RequestLimitReached', 'request_limit_reached')),
    tokenLimitReached: Boolean(pick(row, 'TokenLimitReached', 'token_limit_reached')),
    createdAt: pickTime(row, 'CreatedAt', 'created_at') || '',
    expiresAt: pickTime(row, 'ExpiresAt', 'expires_at'),
    lastUsedAt: pickTime(row, 'LastUsedAt', 'last_used_at'),
    note: pickString(row, 'Note', 'note'),
  }
}

export function mapRequest(raw: unknown): RequestRow {
  const row = asRecord(raw)
  return {
    requestId: pickString(row, 'RequestID', 'request_id'),
    keyId: pickString(row, 'KeyID', 'key_id'),
    keyLabel: pickString(row, 'KeyLabel', 'key_label'),
    keyDisplay: pickString(row, 'KeyDisplay', 'key_display'),
    customer: pickString(row, 'Customer', 'customer'),
    groupId: pickString(row, 'GroupID', 'group_id'),
    groupName: pickString(row, 'GroupName', 'group_name'),
    requestedAt: pickTime(row, 'RequestedAt', 'requested_at') || '',
    completedAt: pickTime(row, 'CompletedAt', 'completed_at'),
    model: pickString(row, 'Model', 'model'),
    requestedModel: pickString(row, 'RequestedModel', 'requested_model'),
    transport: pickString(row, 'Transport', 'transport'),
    protocol: pickString(row, 'Protocol', 'protocol'),
    status: (pickString(row, 'Status', 'status') || 'pending') as RequestRow['status'],
    rejectionCode: pickString(row, 'RejectionCode', 'rejection_code'),
    httpStatus: pickNumber(row, 'HTTPStatus', 'http_status') || null,
    ttftMs: pickOptionalNumber(row, 'TTFTMs', 'ttft_ms'),
    latencyMs: pickOptionalNumber(row, 'LatencyMs', 'latency_ms'),
    tokPerSec: pickOptionalNumber(row, 'TokPerSec', 'tok_per_s'),
    input: pickNumber(row, 'Input', 'input'),
    output: pickNumber(row, 'Output', 'output'),
    reasoning: pickNumber(row, 'Reasoning', 'reasoning'),
    cached: pickNumber(row, 'Cached', 'cached'),
    billed: pickNumber(row, 'Billed', 'billed'),
    provider: pickString(row, 'Provider', 'provider'),
    authId: pickString(row, 'AuthID', 'auth_id'),
    authIndex: pickString(row, 'AuthIndex', 'auth_index'),
    authType: pickString(row, 'AuthType', 'auth_type'),
    effort: pickString(row, 'Effort', 'effort'),
    serviceTier: pickString(row, 'ServiceTier', 'service_tier'),
  }
}

export function emptySummary(): RequestSummary {
  return {
    total: 0, succeeded: 0, failed: 0, canceled: 0, rejected: 0,
    input: 0, output: 0, reasoning: 0, cached: 0, billed: 0,
    avgLatencyMs: 0, avgTtftMs: 0, errorRate: 0,
  }
}

export function mapSummary(raw: unknown): RequestSummary {
  const row = asRecord(raw)
  return {
    total: pickNumber(row, 'total'),
    succeeded: pickNumber(row, 'succeeded'),
    failed: pickNumber(row, 'failed'),
    canceled: pickNumber(row, 'canceled'),
    rejected: pickNumber(row, 'rejected'),
    input: pickNumber(row, 'input'),
    output: pickNumber(row, 'output'),
    reasoning: pickNumber(row, 'reasoning'),
    cached: pickNumber(row, 'cached'),
    billed: pickNumber(row, 'billed'),
    avgLatencyMs: pickNumber(row, 'avg_latency_ms'),
    avgTtftMs: pickNumber(row, 'avg_ttft_ms'),
    errorRate: pickNumber(row, 'error_rate'),
  }
}

export function mapGroup(raw: unknown): ModelGroup {
  const row = asRecord(raw)
  return {
    id: pickString(row, 'id', 'ID'),
    name: pickString(row, 'name', 'Name'),
    description: pickString(row, 'description', 'Description'),
    allModels: Boolean(row.all_models ?? row.AllModels),
    models: Array.isArray(row.models) ? row.models.map(mapGroupModel).filter((item): item is ModelEntry => item != null) : [],
    isSystem: Boolean(row.is_system ?? row.IsSystem),
    keyCount: pickNumber(row, 'key_count', 'KeyCount'),
    createdAt: pickTime(row, 'created_at', 'CreatedAt') || '',
    updatedAt: pickTime(row, 'updated_at', 'UpdatedAt') || '',
  }
}

function mapGroupModel(raw: unknown): ModelEntry | null {
  if (typeof raw === 'string') {
    const name = raw.trim()
    return name ? { name, alias: '' } : null
  }
  const row = asRecord(raw)
  const name = pickString(row, 'name', 'Name').trim()
  if (!name) return null
  return { name, alias: pickString(row, 'alias', 'Alias').trim() }
}

function mapPoint(raw: unknown): UsagePoint {
  const row = asRecord(raw)
  return {
    start: pickString(row, 'start'),
    requests: pickNumber(row, 'requests'),
    succeeded: pickNumber(row, 'succeeded'),
    failed: pickNumber(row, 'failed'),
    rejected: pickNumber(row, 'rejected'),
    canceled: pickNumber(row, 'canceled'),
    input: pickNumber(row, 'input'),
    output: pickNumber(row, 'output'),
    billed: pickNumber(row, 'billed'),
  }
}

function mapRank(raw: unknown): UsageRank {
  const row = asRecord(raw)
  return {
    id: pickString(row, 'id'),
    label: pickString(row, 'label') || pickString(row, 'id'),
    detail: pickString(row, 'detail'),
    requests: pickNumber(row, 'requests'),
    billed: pickNumber(row, 'billed'),
  }
}

export function mapUsage(raw: unknown): UsageReport {
  const row = asRecord(raw)
  const latency = asRecord(row.latency)
  const ranks = (value: unknown) => Array.isArray(value) ? value.map(mapRank) : []
  return {
    from: pickString(row, 'from'),
    to: pickString(row, 'to'),
    bucket: pickString(row, 'bucket') === 'hour' ? 'hour' : 'day',
    summary: mapSummary(row.summary),
    series: Array.isArray(row.series) ? row.series.map(mapPoint) : [],
    topModels: ranks(row.top_models),
    topKeys: ranks(row.top_keys),
    topCustomers: ranks(row.top_customers),
    topAccounts: ranks(row.top_accounts),
    latencyP50: pickNumber(latency, 'latency_p50'),
    latencyP95: pickNumber(latency, 'latency_p95'),
    ttftP50: pickNumber(latency, 'ttft_p50'),
    ttftP95: pickNumber(latency, 'ttft_p95'),
    cacheHitRatio: pickNumber(row, 'cache_hit_ratio'),
  }
}

export function mapStats(raw: unknown): Stats {
  const row = asRecord(raw)
  return {
    keysTotal: pickNumber(row, 'keys_total', 'KeysTotal'),
    keysActive: pickNumber(row, 'keys_active', 'KeysActive'),
    keysRequestLimited: pickNumber(row, 'keys_request_limited', 'KeysRequestLimited'),
    tokensUsedTotal: pickNumber(row, 'tokens_used_total', 'TokensUsedTotal'),
    requestsTotal: pickNumber(row, 'requests_total', 'RequestsTotal'),
    requestsAdmitted: pickNumber(row, 'requests_admitted', 'RequestsAdmitted'),
    requestsSucceeded: pickNumber(row, 'requests_succeeded', 'RequestsSucceeded'),
    requestsFailed: pickNumber(row, 'requests_failed', 'RequestsFailed'),
    requestsCanceled: pickNumber(row, 'requests_canceled', 'RequestsCanceled'),
    requestsRejected: pickNumber(row, 'requests_rejected', 'RequestsRejected'),
    expiringWithin7d: pickNumber(row, 'expiring_within_7d', 'ExpiringWithin7d'),
  }
}

export function mapTunnel(raw: unknown): import('@/types').Tunnel {
  const row = asRecord(raw)
  const kind = pickString(row, 'kind') === 'quick' ? 'quick' : 'token'
  const status = pickString(row, 'status')
  const known = status === 'running' || status === 'starting' || status === 'failed' ? status : 'stopped'
  const desired = pickString(row, 'desired') === 'running' ? 'running' : 'stopped'
  return {
    id: pickString(row, 'id'),
    name: pickString(row, 'name'),
    kind,
    targetUrl: pickString(row, 'target_url'),
    hasToken: Boolean(row.has_token),
    tokenHint: pickString(row, 'token_hint'),
    autostart: Boolean(row.autostart),
    desired,
    status: known,
    pid: pickNumber(row, 'pid'),
    publicUrl: pickString(row, 'public_url'),
    lastError: pickString(row, 'last_error'),
    restarts: pickNumber(row, 'restarts'),
    startedAt: pickTime(row, 'started_at'),
    createdAt: pickString(row, 'created_at'),
    updatedAt: pickString(row, 'updated_at'),
  }
}

export function mapPolicyModels(value: unknown): Array<{ name: string; alias: string }> {
  return Array.isArray(value)
    ? value.map((item) => {
        const row = asRecord(item)
        return {
          name: pickString(row, 'name', 'Name').trim(),
          alias: pickString(row, 'alias', 'Alias').trim(),
        }
      }).filter((item) => item.name)
    : []
}

export function mapDiscoveredModels(value: unknown): string[] {
  let payload: unknown = asRecord(value).data || asRecord(value).models || value
  if (typeof payload === 'string') {
    try {
      payload = JSON.parse(payload)
    } catch {
      return []
    }
    payload = asRecord(payload).data || asRecord(payload).models || payload
  }
  if (!Array.isArray(payload)) return []
  const seen = new Set<string>()
  const names: string[] = []
  for (const item of payload) {
    const name = typeof item === 'string' ? item.trim() : pickString(asRecord(item), 'id', 'name', 'model').trim()
    const key = name.toLowerCase()
    if (!name || seen.has(key)) continue
    seen.add(key)
    names.push(name)
  }
  return names
}
