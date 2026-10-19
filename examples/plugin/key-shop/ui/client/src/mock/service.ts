import type { ClientQuery, ClientSnapshot, Scenario } from '@/types'

const PAGE_SIZE = 5

const key = {
  label: 'Northline Studio',
  display: 'ck_nort…91kx',
  status: 'active' as const,
  createdAt: '2026-08-12T09:18:00',
  expiresAt: '2026-11-12T09:18:00',
  lastUsedAt: '2026-10-18T08:42:00',
  tokensUsed: 18_432_110,
  tokenLimit: 28_000_000,
  requestQuotaUsed: 1_284,
  requestLimit: 5_000,
  requestCount: 1_311,
  requestSuccessCount: 1_204,
  requestFailureCount: 46,
  requestCanceledCount: 34,
  requestRejectedCount: 27,
  requestLimitReached: false,
}

const requests: ClientSnapshot['requests'] = [
  { requestId: 'req_8f21aa', requestedAt: '2026-10-18T08:42:00', model: 'gpt-5-codex', transport: 'websocket', protocol: 'openai-responses', status: 'succeeded', rejectionCode: '', httpStatus: 200, latencyMs: 6120, ttftMs: 842, input: 1840, output: 966, reasoning: 220, cached: 410, billed: 2806 },
  { requestId: 'req_19c0de', requestedAt: '2026-10-18T07:58:00', model: 'claude-sonnet-4-5', transport: 'http', protocol: 'openai-chat', status: 'failed', rejectionCode: '', httpStatus: 502, latencyMs: 2204, ttftMs: 640, input: 420, output: 0, reasoning: 0, cached: 0, billed: 0 },
  { requestId: 'req_77ab10', requestedAt: '2026-10-17T21:14:00', model: 'gemini-2.5-pro', transport: 'http', protocol: 'openai-chat', status: 'succeeded', rejectionCode: '', httpStatus: 200, latencyMs: 4088, ttftMs: 1210, input: 990, output: 640, reasoning: 80, cached: 120, billed: 1630 },
  { requestId: 'req_03ee91', requestedAt: '2026-10-17T16:02:00', model: 'gpt-5.4', transport: 'websocket', protocol: 'openai-responses', status: 'canceled', rejectionCode: '', httpStatus: 499, latencyMs: 1800, ttftMs: 210, input: 80, output: 0, reasoning: 0, cached: 0, billed: 0 },
  { requestId: 'req_b44120', requestedAt: '2026-10-16T11:26:00', model: 'gpt-5-codex(high)', transport: 'websocket', protocol: 'openai-responses', status: 'succeeded', rejectionCode: '', httpStatus: 200, latencyMs: 7344, ttftMs: 990, input: 2104, output: 1330, reasoning: 480, cached: 260, billed: 3434 },
  { requestId: 'req_c90877', requestedAt: '2026-10-15T09:40:00', model: 'gpt-5.4-mini', transport: 'http', protocol: 'openai-chat', status: 'rejected', rejectionCode: 'model_not_allowed', httpStatus: 403, latencyMs: 36, ttftMs: null, input: 0, output: 0, reasoning: 0, cached: 0, billed: 0 },
  { requestId: 'req_4410ac', requestedAt: '2026-10-14T18:33:00', model: 'claude-opus-4-1', transport: 'http', protocol: 'openai-chat', status: 'pending', rejectionCode: '', httpStatus: null, latencyMs: null, ttftMs: null, input: 0, output: 0, reasoning: 0, cached: 0, billed: 0 },
]

const models = ['codex-latest', 'claude-sonnet-4-5', 'gemini-2.5-pro', 'gpt-5.4-mini', 'gpt-5.4', 'claude-opus-4-1', 'gemini-2.5-flash', 'gpt-5-codex', 'claude-haiku-4-5', 'o3', 'o4-mini', 'gemini-2.5-flash-lite', 'gpt-5.3-codex', 'claude-sonnet-4', 'grok-4', 'llama-4-maverick', 'deepseek-v3', 'mistral-large', 'command-a', 'qwen3-coder']

function pageOf(rows: ClientSnapshot['requests'], page: number): Pick<ClientSnapshot, 'requests' | 'requestTotal' | 'requestHasMore'> {
  const start = (page - 1) * PAGE_SIZE
  return {
    requests: rows.slice(start, start + PAGE_SIZE),
    requestTotal: rows.length,
    requestHasMore: start + PAGE_SIZE < rows.length,
  }
}

export function createClientService() {
  return {
    pageSize: PAGE_SIZE,
    async authenticate(secret: string, _scenario?: Scenario) {
      if (!secret.trim()) throw new Error('Enter an API key to continue.')
      if (secret.trim().toLowerCase() === 'bad') throw new Error('That key could not be authenticated.')
    },
    async load(input: ClientQuery): Promise<ClientSnapshot> {
      if (input.scenario === 'error') throw new Error('The workspace could not be loaded.')
      if (input.scenario === 'empty') {
        return {
          key: { ...key, tokensUsed: 0, requestQuotaUsed: 0, requestCount: 0, requestSuccessCount: 0, requestFailureCount: 0, requestCanceledCount: 0, requestRejectedCount: 0, lastUsedAt: null },
          models: [],
          group: null,
          modelAccessMode: 'full',
          baseUrl: 'https://relay.northline.example/v1',
          ...pageOf([], input.page),
        }
      }
      if (input.scenario === 'limited') {
        return {
          key: { ...key, requestQuotaUsed: 5000, requestLimit: 5000, requestLimitReached: true },
          models: ['codex-latest', 'claude-sonnet-4-5', 'gemini-2.5-pro'],
          group: { id: 'grp_studio', name: 'Studio', allModels: false },
          modelAccessMode: 'custom',
          baseUrl: 'https://relay.northline.example/v1',
          ...pageOf(requests, input.page),
        }
      }
      return {
        key,
        models,
        group: { id: 'grp_studio', name: 'Studio', allModels: false },
        modelAccessMode: 'custom',
        baseUrl: 'https://relay.northline.example/v1',
        ...pageOf(requests, input.page),
      }
    },
  }
}

export type ClientService = ReturnType<typeof createClientService>
