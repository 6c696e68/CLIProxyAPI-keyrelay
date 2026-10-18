export type Scenario = 'ready' | 'loading' | 'empty' | 'error' | 'limited'
export type ThemeName = 'light' | 'dark'
export type KeyStatus = 'active' | 'disabled' | 'expired' | 'exhausted'
export type RequestStatus = 'succeeded' | 'failed' | 'pending' | 'rejected' | 'canceled'
export type PolicyMode = 'full' | 'custom'

export interface KeyRecord {
  id: string
  label: string
  display: string
  customer: string
  status: KeyStatus
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
  tokenLimitReached: boolean
  createdAt: string
  expiresAt: string | null
  lastUsedAt: string | null
  note: string
}

export interface RequestRow {
  requestId: string
  keyId: string
  requestedAt: string
  model: string
  transport: string
  protocol: string
  status: RequestStatus
  rejectionCode: string
  httpStatus: number | null
  ttftMs: number | null
  latencyMs: number | null
  input: number
  output: number
  reasoning: number
  cached: number
  billed: number
}

export interface ModelEntry {
  name: string
  alias: string
}

export interface Policy {
  mode: PolicyMode
  models: ModelEntry[]
  loaded: boolean
}

export interface Stats {
  keysTotal: number
  keysActive: number
  keysRequestLimited: number
  tokensUsedTotal: number
  requestsTotal: number
  requestsAdmitted: number
  requestsSucceeded: number
  requestsFailed: number
  requestsCanceled: number
  requestsRejected: number
  expiringWithin7d: number
}

export interface AdminQuery {
  scenario: Scenario
  query: string
  status: string
  page: number
  selectedId: string
  requestPage: number
}

export interface AdminSnapshot {
  stats: Stats
  keys: KeyRecord[]
  keysTotal: number
  requests: RequestRow[]
  requestTotal: number
  requestHasMore: boolean
  policy: Policy
  availableModels: string[]
}

export interface CreateDraft {
  label: string
  customer: string
  note: string
  count: number
  expiresLocal: string
  neverExpires: boolean
  tokenLimit: number
  unlimitedTokens: boolean
  requestLimit: number
  unlimitedRequests: boolean
}

export interface EditDraft {
  id: string
  label: string
  customer: string
  note: string
  status: 'active' | 'disabled'
  expiresLocal: string
  neverExpires: boolean
  tokenLimit: number
  requestLimit: number
  unlimitedRequests: boolean
}
