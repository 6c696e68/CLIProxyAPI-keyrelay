import type { ClientQuery, ClientSnapshot, ModelUsageRow, Scenario } from '@/types'
import { asRecord, pickNumber, pickString, pickTime } from './pick'
import { mapKey, mapModelUsage, mapModels, mapRequest, mapSessionGroup } from './map'

export const CLIENT_STORAGE = 'keyshop.client'
const SESSION_PATH = '/client/api/session?limit=25'
const PAGE_SIZE = 25

async function readJSON(response: Response): Promise<Record<string, unknown>> {
  const text = await response.text()
  try {
    return asRecord(JSON.parse(text))
  } catch {
    throw new Error('The server returned an unreadable response.')
  }
}

export function createLiveClientService() {
  let secret = ''
  const cursors = ['']
  // Model usage is a page-1 field; later pages reuse the last known value.
  let modelUsage: ModelUsageRow[] = []
  let modelUsageSince = ''

  async function session(page: number): Promise<ClientSnapshot> {
    const cursor = page <= 1 ? '' : cursors[page - 1] || ''
    if (page > 1 && !cursor) throw new Error('Unable to load requests.')
    const response = await fetch(SESSION_PATH + (cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''), {
      headers: { Authorization: `Bearer ${secret}` },
    })
    const data = await readJSON(response)
    if (!response.ok) throw new Error(pickString(data, 'error') || 'Unable to open workspace.')
    const next = pickString(data, 'next_cursor')
    if (next) cursors[page] = next
    const key = mapKey(data.Key || data.key)
    const requests = Array.isArray(data.Requests) ? data.Requests.map(mapRequest) : Array.isArray(data.requests) ? data.requests.map(mapRequest) : []
    const mode = pickString(data, 'model_access_mode') === 'custom' ? 'custom' : 'full'
    const usage = data.model_usage ?? data.ModelUsage
    if (Array.isArray(usage)) {
      modelUsage = mapModelUsage(usage)
      modelUsageSince = pickTime(data, 'model_usage_since', 'ModelUsageSince') || ''
    }
    return {
      key,
      requests,
      models: mapModels({ models: data.models }),
      group: mapSessionGroup(data.group),
      modelAccessMode: mode,
      baseUrl: pickString(data, 'base_url') || `${window.location.origin}/v1`,
      requestTotal: pickNumber(data, 'total'),
      requestHasMore: Boolean(data.has_more),
      modelUsage,
      modelUsageSince,
    }
  }

  return {
    pageSize: PAGE_SIZE,
    async authenticate(value: string, _scenario?: Scenario) {
      const next = value.trim()
      if (!next) throw new Error('Enter an API key to continue.')
      secret = next
      cursors.length = 0
      cursors.push('')
      modelUsage = []
      modelUsageSince = ''
      await session(1)
    },
    async load(input: ClientQuery): Promise<ClientSnapshot> {
      if (!secret) throw new Error('Enter an API key to continue.')
      if (input.page <= 1) {
        cursors.length = 0
        cursors.push('')
      }
      return session(input.page)
    },
  }
}

export type LiveClientService = ReturnType<typeof createLiveClientService>
