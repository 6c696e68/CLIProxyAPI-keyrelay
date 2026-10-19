export type Scenario = 'ready' | 'loading' | 'empty' | 'error' | 'limited'
export type ThemeName = 'light' | 'dark'
export type RequestStatus = 'succeeded' | 'failed' | 'pending' | 'rejected' | 'canceled'

export interface ClientKey {
  label: string
  display: string
  status: 'active' | 'expired' | 'exhausted' | 'disabled'
  createdAt: string
  expiresAt: string | null
  lastUsedAt: string | null
  tokensUsed: number
  tokenLimit: number
  requestQuotaUsed: number
  requestLimit: number
  requestCount: number
  requestSuccessCount: number
  requestFailureCount: number
  requestCanceledCount: number
  requestRejectedCount: number
  requestLimitReached: boolean
}

export interface RequestRow {
  requestId: string
  requestedAt: string
  model: string
  transport: string
  protocol: string
  status: RequestStatus
  rejectionCode: string
  httpStatus: number | null
  latencyMs: number | null
  ttftMs: number | null
  input: number
  output: number
  reasoning: number
  cached: number
  billed: number
}

export interface ClientQuery {
  scenario: Scenario
  page: number
}

export interface SessionGroup {
  id: string
  name: string
  allModels: boolean
}

export interface ClientSnapshot {
  key: ClientKey
  requests: RequestRow[]
  models: string[]
  group: SessionGroup | null
  modelAccessMode: 'full' | 'custom'
  baseUrl: string
  requestTotal: number
  requestHasMore: boolean
}
