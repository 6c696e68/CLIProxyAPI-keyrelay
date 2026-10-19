import type { BulkPatch } from '@/types'

export function bulkPayload(ids: string[], patch: BulkPatch): Record<string, unknown> {
  const body: Record<string, unknown> = { ids }
  if (patch.groupId != null) body.group_id = patch.groupId
  if (patch.status) body.status = patch.status
  if (patch.customer != null) body.customer = patch.customer
  if (patch.note != null) body.note = patch.note
  if (patch.neverExpires) body.never_expires = true
  else if (patch.ttlSeconds != null) body.ttl_seconds = patch.ttlSeconds
  if (patch.unlimitedTokens) body.unlimited_tokens = true
  else if (patch.tokenLimit != null) body.token_limit = patch.tokenLimit
  if (patch.unlimitedRequests) body.unlimited_requests = true
  else if (patch.requestLimit != null) body.request_limit = patch.requestLimit
  return body
}
