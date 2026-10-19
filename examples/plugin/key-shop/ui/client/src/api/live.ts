import type { ClientQuery, ClientSnapshot, Scenario } from '@/types'
import { asRecord, pickNumber, pickString } from './pick'
import { mapKey, mapModels, mapRequest, mapSessionGroup } from './map'

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
    return {
      key,
      requests,
      models: mapModels({ models: data.models }),
      group: mapSessionGroup(data.group),
      modelAccessMode: mode,
      baseUrl: pickString(data, 'base_url') || `${window.location.origin}/v1`,
      requestTotal: pickNumber(data, 'total'),
      requestHasMore: Boolean(data.has_more),
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
