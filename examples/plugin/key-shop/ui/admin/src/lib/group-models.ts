import type { ModelEntry } from '@/types'

// groupModelProblems flags aliases that collide with another name or alias.
// An alias equal to its own upstream name is treated as unset.
export function groupModelProblems(models: ModelEntry[]): Record<string, string> {
  const problems: Record<string, string> = {}
  const names = new Set(models.map((item) => item.name.trim().toLowerCase()).filter(Boolean))
  const seenAlias = new Map<string, string>()
  for (const item of models) {
    const alias = item.alias.trim()
    const own = item.name.trim().toLowerCase()
    if (!alias || alias.toLowerCase() === own) continue
    const key = alias.toLowerCase()
    if (names.has(key)) {
      problems[item.name] = 'Alias matches another model name'
      continue
    }
    const prior = seenAlias.get(key)
    if (prior) {
      problems[item.name] = 'Alias is already used'
      problems[prior] = problems[prior] || 'Alias is already used'
      continue
    }
    seenAlias.set(key, item.name)
  }
  return problems
}
