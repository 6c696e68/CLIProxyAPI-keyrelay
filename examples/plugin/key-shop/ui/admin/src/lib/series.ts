import type { UsageBucket, UsagePoint } from '@/types'

function floorUtc(date: Date, bucket: UsageBucket): Date {
  const next = new Date(date)
  next.setUTCMinutes(0, 0, 0)
  if (bucket === 'day') next.setUTCHours(0, 0, 0, 0)
  return next
}

function prefix(value: string, bucket: UsageBucket): string {
  return value.slice(0, bucket === 'hour' ? 13 : 10)
}

export function fillSeries(from: string, to: string, bucket: UsageBucket, series: UsagePoint[]): UsagePoint[] {
  if (!from || !to) return series
  const start = floorUtc(new Date(from), bucket)
  const end = new Date(to)
  if (Number.isNaN(start.valueOf()) || Number.isNaN(end.valueOf()) || start > end) return series
  const byPrefix = new Map(series.map((point) => [prefix(point.start, bucket), point]))
  const step = bucket === 'hour' ? 3_600_000 : 86_400_000
  const filled: UsagePoint[] = []
  for (let time = start.getTime(); time <= end.getTime() && filled.length < 400; time += step) {
    const stamp = new Date(time).toISOString()
    const found = byPrefix.get(prefix(stamp, bucket))
    filled.push(found || {
      start: stamp,
      requests: 0,
      succeeded: 0,
      failed: 0,
      rejected: 0,
      canceled: 0,
      input: 0,
      output: 0,
      billed: 0,
    })
  }
  return filled.length ? filled : series
}
