import type { KeyRecord, RequestRow, Stats } from '@/types'
import { asRecord, pick, pickNumber, pickString, pickTime } from './pick'

export function mapKey(raw: unknown): KeyRecord {
  const row = asRecord(raw)
  return {
    id: pickString(row, 'ID', 'id'),
    label: pickString(row, 'Label', 'label'),
    display: pickString(row, 'Display', 'display'),
    customer: pickString(row, 'Customer', 'customer'),
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
    requestedAt: pickTime(row, 'RequestedAt', 'requested_at') || '',
    model: pickString(row, 'Model', 'model'),
    transport: pickString(row, 'Transport', 'transport'),
    protocol: pickString(row, 'Protocol', 'protocol'),
    status: (pickString(row, 'Status', 'status') || 'pending') as RequestRow['status'],
    rejectionCode: pickString(row, 'RejectionCode', 'rejection_code'),
    httpStatus: pickNumber(row, 'HTTPStatus', 'http_status') || null,
    ttftMs: pickNumber(row, 'TTFTMs', 'ttft_ms') || null,
    latencyMs: pickNumber(row, 'LatencyMs', 'latency_ms') || null,
    input: pickNumber(row, 'Input', 'input'),
    output: pickNumber(row, 'Output', 'output'),
    reasoning: pickNumber(row, 'Reasoning', 'reasoning'),
    cached: pickNumber(row, 'Cached', 'cached'),
    billed: pickNumber(row, 'Billed', 'billed'),
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
