import { asRecord } from './pick'

export const MANAGEMENT_STORAGE = 'keyshop.mgmt'

function decode(payload: string): string {
  if (!payload || !payload.startsWith('enc::v1::')) return payload
  const binary = atob(payload.slice(8))
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
  const secret = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${location.host}|${navigator.userAgent}`)
  const out = new Uint8Array(bytes.length)
  for (let index = 0; index < bytes.length; index += 1) out[index] = bytes[index] ^ secret[index % secret.length]
  return new TextDecoder().decode(out)
}

export function panelKey(): string {
  const stores: Storage[] = [localStorage]
  try {
    if (parent !== window) stores.push(parent.localStorage)
  } catch {
    // iframe cannot read the parent store
  }
  for (const store of stores) {
    for (const name of ['cli-proxy-auth', 'managementKey']) {
      try {
        const raw = store.getItem(name)
        if (!raw) continue
        const parsed = JSON.parse(decode(raw))
        const state = parsed?.state || parsed
        const key = state?.managementKey || state
        if (typeof key === 'string' && key.trim()) return key.trim()
      } catch {
        // keep scanning other stores
      }
    }
  }
  return sessionStorage.getItem(MANAGEMENT_STORAGE) || ''
}

export async function managementRequest(key: string, method: string, path: string, body?: unknown): Promise<Record<string, unknown>> {
  let response: Response
  try {
    response = await fetch(path, {
      method,
      headers: {
        Authorization: `Bearer ${key}`,
        'Content-Type': 'application/json',
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(8000),
    })
  } catch (error) {
    const name = error instanceof Error ? error.name : ''
    if (name === 'TimeoutError' || name === 'AbortError') throw new Error('The operations API did not respond in time.')
    throw new Error('The operations API did not respond.')
  }
  let data: Record<string, unknown> = {}
  try {
    data = asRecord(await response.json())
  } catch {
    data = {}
  }
  if (!response.ok) {
    const err = data.error
    const message = typeof err === 'string' ? err : pickMessage(err) || pickMessage(data.message) || 'Request failed'
    throw new Error(message)
  }
  return data
}

function pickMessage(value: unknown): string {
  if (typeof value === 'string' && value.trim()) return value
  if (value && typeof value === 'object' && 'message' in value && typeof value.message === 'string') return value.message
  return ''
}
