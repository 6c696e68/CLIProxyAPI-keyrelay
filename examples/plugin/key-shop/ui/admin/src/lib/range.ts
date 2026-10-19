import type { UsageBucket } from '@/types'

export type RangePreset = '1h' | '24h' | '7d' | '30d' | 'all'

export function presetRange(preset: RangePreset, now = new Date()): { from: string; to: string; bucket: UsageBucket } {
  if (preset === 'all') return { from: '', to: '', bucket: 'day' }
  const to = now.toISOString()
  const fromDate = new Date(now)
  if (preset === '1h') fromDate.setTime(fromDate.getTime() - 3_600_000)
  else if (preset === '24h') fromDate.setTime(fromDate.getTime() - 86_400_000)
  else if (preset === '7d') fromDate.setTime(fromDate.getTime() - 7 * 86_400_000)
  else fromDate.setTime(fromDate.getTime() - 30 * 86_400_000)
  const span = now.getTime() - fromDate.getTime()
  return { from: fromDate.toISOString(), to, bucket: span <= 48 * 3_600_000 ? 'hour' : 'day' }
}
