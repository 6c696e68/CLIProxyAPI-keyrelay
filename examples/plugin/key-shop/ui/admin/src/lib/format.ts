export function formatNumber(value: number | null | undefined): string {
  return Number(value || 0).toLocaleString('en-US')
}

export function formatWhen(value: string | null | undefined, empty = 'Never'): string {
  if (!value) return empty
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return value
  return date.toLocaleString('en-US', { dateStyle: 'medium', timeStyle: 'short' })
}

export function splitWhen(value: string | null | undefined): { date: string; time: string; full: string } {
  if (!value) return { date: '—', time: '', full: '—' }
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return { date: value, time: '', full: value }
  return {
    date: date.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' }),
    time: date.toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' }),
    full: formatWhen(value),
  }
}

export function quota(used: number, limit: number): string {
  return `${formatNumber(used)} / ${limit ? formatNumber(limit) : '∞'}`
}

export function requestBreakdown(key: {
  requestSuccessCount: number
  requestFailureCount: number
  requestCanceledCount: number
  requestRejectedCount: number
}): string {
  return `${formatNumber(key.requestSuccessCount)} ok · ${formatNumber(key.requestFailureCount)} fail · ${formatNumber(key.requestCanceledCount)} cancel · ${formatNumber(key.requestRejectedCount)} reject`
}

export function rejectionLabel(code: string): string {
  const labels: Record<string, string> = {
    request_limit_exceeded: 'Request limit exceeded',
    token_limit_exceeded: 'Token limit exceeded',
    model_not_allowed: 'Model not allowed',
    key_expired: 'Key expired',
    key_disabled: 'Key disabled',
    key_not_found: 'Key not found',
  }
  return labels[code] || code || '—'
}

export function statusLabel(value: string): string {
  const labels: Record<string, string> = {
    active: 'Active',
    disabled: 'Disabled',
    expired: 'Expired',
    exhausted: 'Exhausted',
    succeeded: 'Succeeded',
    failed: 'Failed',
    pending: 'Pending',
    rejected: 'Rejected',
    canceled: 'Canceled',
    cancelled: 'Canceled',
  }
  const status = String(value || 'unknown').toLowerCase()
  return labels[status] || status.charAt(0).toUpperCase() + status.slice(1)
}

export function statusTone(value: string): 'good' | 'warn' | 'bad' | 'muted' {
  const status = String(value || '').toLowerCase()
  if (['active', 'succeeded', 'ok', 'completed'].includes(status)) return 'good'
  if (['expired', 'exhausted', 'canceled', 'cancelled'].includes(status)) return 'warn'
  if (['failed', 'rejected'].includes(status)) return 'bad'
  return 'muted'
}

export function pageRange(page: number, count: number, total: number, size = 5): string {
  if (!count || !total) return `0 of ${total}`
  const start = (Math.max(1, page) - 1) * size + 1
  return `${start}–${start + count - 1} of ${total}`
}

export function randomLabel(): string {
  const bytes = new Uint8Array(4)
  crypto.getRandomValues(bytes)
  return `key-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
}

export async function copyText(value: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(value)
    return true
  } catch {
    const area = document.createElement('textarea')
    area.value = value
    area.setAttribute('readonly', '')
    area.style.position = 'fixed'
    area.style.opacity = '0'
    document.body.appendChild(area)
    area.select()
    const copied = document.execCommand('copy')
    area.remove()
    return copied
  }
}
