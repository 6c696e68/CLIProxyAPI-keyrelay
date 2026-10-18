const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

export function pad(value: number): string {
  return String(value).padStart(2, '0')
}

export function toLocalInput(date: Date): string {
  if (Number.isNaN(date.valueOf())) return ''
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function parseLocalInput(value: string): Date | null {
  if (!value) return null
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? null : date
}

export function prettyDateTime(value: string): string {
  const date = parseLocalInput(value)
  if (!date) return 'No date chosen'
  return `${date.getDate()} ${months[date.getMonth()]} ${date.getFullYear()} · ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function startOfMonth(date: Date): Date {
  return new Date(date.getFullYear(), date.getMonth(), 1)
}

export function addMonths(date: Date, amount: number): Date {
  return new Date(date.getFullYear(), date.getMonth() + amount, 1)
}

export function monthLabel(date: Date): string {
  return `${months[date.getMonth()]} ${date.getFullYear()}`
}

export function monthCells(view: Date): Array<Date | null> {
  const first = startOfMonth(view)
  const lead = first.getDay()
  const days = new Date(view.getFullYear(), view.getMonth() + 1, 0).getDate()
  const cells: Array<Date | null> = Array.from({ length: lead }, () => null)
  for (let day = 1; day <= days; day += 1) cells.push(new Date(view.getFullYear(), view.getMonth(), day))
  while (cells.length % 7 !== 0) cells.push(null)
  return cells
}

export function sameDay(left: Date | null, right: Date | null): boolean {
  if (!left || !right) return false
  return left.getFullYear() === right.getFullYear() && left.getMonth() === right.getMonth() && left.getDate() === right.getDate()
}

export function withTime(day: Date, hours: number, minutes: number): string {
  const next = new Date(day)
  next.setHours(hours, minutes, 0, 0)
  return toLocalInput(next)
}

export const expiryPresets: Array<[string, number]> = [
  ['1 day', 86_400],
  ['3 days', 259_200],
  ['7 days', 604_800],
  ['14 days', 1_209_600],
  ['30 days', 2_592_000],
  ['90 days', 7_776_000],
  ['1 year', 31_536_000],
]
