import { toLocalInput } from '@/lib/datetime'
import type { CreateDraft } from '@/types'

export const LAST_GROUP_KEY = 'keyshop.admin.lastGroup'

export function defaultCreateDraft(groupId = ''): CreateDraft {
  return {
    label: '',
    customer: '',
    note: '',
    count: 1,
    groupId,
    expiresLocal: toLocalInput(new Date(Date.now() + 7 * 86_400_000)),
    neverExpires: false,
    tokenLimit: 100_000_000,
    unlimitedTokens: false,
    requestLimit: 0,
    unlimitedRequests: true,
  }
}

export function rememberedGroup(ids: string[]): string {
  const last = localStorage.getItem(LAST_GROUP_KEY) || ''
  if (last && ids.includes(last)) return last
  return ids[0] || ''
}

export function rememberGroup(id: string) {
  if (id) localStorage.setItem(LAST_GROUP_KEY, id)
}
