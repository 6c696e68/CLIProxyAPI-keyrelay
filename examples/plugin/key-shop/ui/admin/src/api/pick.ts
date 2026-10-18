export function pick(source: Record<string, unknown> | null | undefined, ...keys: string[]): unknown {
  if (!source) return undefined
  for (const key of keys) {
    const value = source[key]
    if (value != null && value !== '') return value
  }
  return undefined
}

export function pickNumber(source: Record<string, unknown> | null | undefined, ...keys: string[]): number {
  return Number(pick(source, ...keys) || 0)
}

export function pickString(source: Record<string, unknown> | null | undefined, ...keys: string[]): string {
  const value = pick(source, ...keys)
  return value == null ? '' : String(value)
}

export function pickTime(source: Record<string, unknown> | null | undefined, ...keys: string[]): string | null {
  const value = pick(source, ...keys)
  if (value == null || value === '') return null
  const text = String(value)
  if (text.startsWith('0001-01-01')) return null
  return text
}

export function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}
