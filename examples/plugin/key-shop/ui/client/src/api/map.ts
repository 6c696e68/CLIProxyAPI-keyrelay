import type { ClientKey, RequestRow, SessionGroup } from '@/types'
import { asRecord, pick, pickNumber, pickString, pickTime } from './pick'

export function mapKey(raw: unknown): ClientKey {
  const row = asRecord(raw)
  return {
    label: pickString(row, 'Label', 'label'),
    display: pickString(row, 'Display', 'display'),
    status: (pickString(row, 'Status', 'status') || 'active') as ClientKey['status'],
    createdAt: pickTime(row, 'CreatedAt', 'created_at') || '',
    expiresAt: pickTime(row, 'ExpiresAt', 'expires_at'),
    lastUsedAt: pickTime(row, 'LastUsedAt', 'last_used_at'),
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
  }
}

export function mapRequest(raw: unknown): RequestRow {
  const row = asRecord(raw)
  return {
    requestId: pickString(row, 'RequestID', 'request_id'),
    requestedAt: pickTime(row, 'RequestedAt', 'requested_at') || '',
    model: pickString(row, 'Model', 'model'),
    transport: pickString(row, 'Transport', 'transport'),
    protocol: pickString(row, 'Protocol', 'protocol'),
    status: (pickString(row, 'Status', 'status') || 'pending') as RequestRow['status'],
    rejectionCode: pickString(row, 'RejectionCode', 'rejection_code'),
    httpStatus: pickNumber(row, 'HTTPStatus', 'http_status') || null,
    latencyMs: pickNumber(row, 'LatencyMs', 'latency_ms') || null,
    ttftMs: pickNumber(row, 'TTFTMs', 'ttft_ms') || null,
    input: pickNumber(row, 'Input', 'input'),
    output: pickNumber(row, 'Output', 'output'),
    reasoning: pickNumber(row, 'Reasoning', 'reasoning'),
    cached: pickNumber(row, 'Cached', 'cached'),
    billed: pickNumber(row, 'Billed', 'billed'),
  }
}

// Customer pages must not map upstream account fields (auth_id, provider, auth_index).

export function mapSessionGroup(value: unknown): SessionGroup | null {
  if (!value || typeof value !== 'object') return null
  const row = asRecord(value)
  const id = pickString(row, 'id', 'ID')
  const name = pickString(row, 'name', 'Name')
  if (!id && !name) return null
  return { id, name: name || 'Model group', allModels: Boolean(row.all_models ?? row.AllModels) }
}

export function mapModels(value: unknown): string[] {
  const row = asRecord(value)
  const list = row.data || row.models || value
  if (!Array.isArray(list)) return []
  return list.map((item) => typeof item === 'string' ? item : pickString(asRecord(item), 'id', 'name')).filter(Boolean)
}
