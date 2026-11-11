import type { ClientQuery, ClientSnapshot, ModelUsageRow, Scenario } from '@/types'

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

const modelUsage: ModelUsageRow[] = [
  { model: 'gpt-5-codex', requests: 482, inputTokens: 812_400_000, outputTokens: 392_480_000, reasoningTokens: 96_400_000, cachedTokens: 402_100_000, totalTokens: 1_204_880_000, firstUsedAt: '2026-09-18T10:04:00', lastUsedAt: '2026-10-18T08:42:00' },
  { model: 'claude-sonnet-4-5', requests: 302, inputTokens: 4_120_600, outputTokens: 1_982_400, reasoningTokens: 0, cachedTokens: 1_240_900, totalTokens: 6_103_000, firstUsedAt: '2026-09-19T11:20:00', lastUsedAt: '2026-10-18T07:58:00' },
  { model: 'gemini-2.5-pro', requests: 208, inputTokens: 2_884_100, outputTokens: 1_402_600, reasoningTokens: 318_400, cachedTokens: 604_200, totalTokens: 4_286_700, firstUsedAt: '2026-09-20T08:12:00', lastUsedAt: '2026-10-17T21:14:00' },
  { model: 'gemini-2.5-flash-preview-thinking-09-2025-extended-context', requests: 121, inputTokens: 1_846_200, outputTokens: 902_400, reasoningTokens: 214_800, cachedTokens: 0, totalTokens: 2_748_600, firstUsedAt: '2026-09-22T15:40:00', lastUsedAt: '2026-10-17T19:02:00' },
  { model: 'gpt-5.4', requests: 86, inputTokens: 1_204_800, outputTokens: 688_200, reasoningTokens: 142_600, cachedTokens: 402_800, totalTokens: 1_893_000, firstUsedAt: '2026-09-25T09:10:00', lastUsedAt: '2026-10-17T16:02:00' },
  { model: 'claude-opus-4-1', requests: 54, inputTokens: 684_900, outputTokens: 388_400, reasoningTokens: 128_200, cachedTokens: 214_600, totalTokens: 1_073_300, firstUsedAt: '2026-09-28T13:24:00', lastUsedAt: '2026-10-15T18:33:00' },
  { model: 'gpt-5-codex(high)', requests: 31, inputTokens: 612_400, outputTokens: 448_900, reasoningTokens: 302_400, cachedTokens: 204_100, totalTokens: 1_061_300, firstUsedAt: '2026-10-02T10:50:00', lastUsedAt: '2026-10-16T11:26:00' },
  { model: 'o4-mini', requests: 18, inputTokens: 244_600, outputTokens: 121_800, reasoningTokens: 84_200, cachedTokens: 42_600, totalTokens: 366_400, firstUsedAt: '2026-10-06T17:08:00', lastUsedAt: '2026-10-14T09:52:00' },
  { model: 'gemini-2.5-flash-lite', requests: 9, inputTokens: 96_400, outputTokens: 42_100, reasoningTokens: 0, cachedTokens: 18_400, totalTokens: 138_500, firstUsedAt: '2026-10-09T12:36:00', lastUsedAt: '2026-10-12T07:15:00' },
]

const modelUsageSince = '2026-09-18T00:00:00'

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
          modelUsage: [],
          modelUsageSince: '',
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
          modelUsage,
          modelUsageSince,
          ...pageOf(requests, input.page),
        }
      }
      return {
        key,
        models,
        group: { id: 'grp_studio', name: 'Studio', allModels: false },
        modelAccessMode: 'custom',
        baseUrl: 'https://relay.northline.example/v1',
        modelUsage,
        modelUsageSince,
        ...pageOf(requests, input.page),
      }
    },
  }
}

export type ClientService = ReturnType<typeof createClientService>
