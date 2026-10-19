export type Scenario = 'ready' | 'loading' | 'empty' | 'error' | 'limited'
export type ThemeName = 'light' | 'dark'
export type KeyStatus = 'active' | 'disabled' | 'expired' | 'exhausted'
export type RequestStatus = 'succeeded' | 'failed' | 'pending' | 'rejected' | 'canceled'
export type PolicyMode = 'full' | 'custom'
export type AdminRoute = 'dashboard' | 'groups' | 'keys' | 'requests'
export type UsageBucket = 'hour' | 'day'

export interface KeyRecord {
  id: string
  label: string
  display: string
  customer: string
  groupId: string
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
  keyLabel: string
  keyDisplay: string
  customer: string
  groupId: string
  groupName: string
  requestedAt: string
  completedAt: string | null
  model: string
  requestedModel: string
  transport: string
  protocol: string
  status: RequestStatus
  rejectionCode: string
  httpStatus: number | null
  ttftMs: number | null
  latencyMs: number | null
  tokPerSec: number | null
  input: number
  output: number
  reasoning: number
  cached: number
  billed: number
  provider: string
  authId: string
  authIndex: string
  authType: string
  effort: string
  serviceTier: string
}

export interface RequestSummary {
  total: number
  succeeded: number
  failed: number
  canceled: number
  rejected: number
  input: number
  output: number
  reasoning: number
  cached: number
  billed: number
  avgLatencyMs: number
  avgTtftMs: number
  errorRate: number
}

export interface RequestPage {
  requests: RequestRow[]
  nextCursor: string
  hasMore: boolean
  total: number
  summary: RequestSummary
}

export interface RequestQuery {
  id: string
  customer: string
  group: string
  status: string
  model: string
  account: string
  effort: string
  code: string
  q: string
  from: string
  to: string
  limit: number
  cursor: string
}

export interface KeysQuery {
  q: string
  status: string
  group: string
  page: number
  pageSize: number
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

export interface ModelGroup {
  id: string
  name: string
  description: string
  allModels: boolean
  models: ModelEntry[]
  isSystem: boolean
  keyCount: number
  createdAt: string
  updatedAt: string
}

export interface GroupInput {
  id?: string
  name: string
  description: string
  allModels: boolean
  models: ModelEntry[]
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

export interface UsagePoint {
  start: string
  requests: number
  succeeded: number
  failed: number
  rejected: number
  canceled: number
  input: number
  output: number
  billed: number
}

export interface UsageRank {
  id: string
  label: string
  detail: string
  requests: number
  billed: number
}

export interface UsageReport {
  from: string
  to: string
  bucket: UsageBucket
  summary: RequestSummary
  series: UsagePoint[]
  topModels: UsageRank[]
  topKeys: UsageRank[]
  topCustomers: UsageRank[]
  topAccounts: UsageRank[]
  latencyP50: number
  latencyP95: number
  ttftP50: number
  ttftP95: number
  cacheHitRatio: number
}

export interface UsageQuery {
  from: string
  to: string
  bucket: UsageBucket
}

export interface CreateDraft {
  label: string
  customer: string
  note: string
  count: number
  groupId: string
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
  groupId: string
  status: 'active' | 'disabled'
  expiresLocal: string
  neverExpires: boolean
  tokenLimit: number
  requestLimit: number
  unlimitedRequests: boolean
}

export interface BulkPatch {
  groupId?: string
  status?: 'active' | 'disabled'
  customer?: string
  note?: string
  ttlSeconds?: number
  neverExpires?: boolean
  tokenLimit?: number
  unlimitedTokens?: boolean
  requestLimit?: number
  unlimitedRequests?: boolean
}

export interface AdminService {
  pageSize: number
  authenticate(secret: string): Promise<void>
  stats(scenario?: Scenario): Promise<Stats>
  usage(query: UsageQuery, scenario?: Scenario): Promise<UsageReport>
  policy(scenario?: Scenario): Promise<Policy>
  savePolicy(next: Policy, scenario?: Scenario): Promise<void>
  groups(scenario?: Scenario): Promise<ModelGroup[]>
  saveGroup(input: GroupInput, scenario?: Scenario): Promise<ModelGroup>
  removeGroup(id: string, scenario?: Scenario): Promise<void>
  catalog(scenario?: Scenario): Promise<string[]>
  keys(query: KeysQuery, scenario?: Scenario): Promise<{ keys: KeyRecord[]; total: number }>
  create(draft: CreateDraft, scenario?: Scenario): Promise<string[]>
  update(draft: EditDraft, scenario?: Scenario): Promise<void>
  setEnabled(id: string, enabled: boolean, scenario?: Scenario): Promise<void>
  rotate(id: string, scenario?: Scenario): Promise<string>
  resetUsage(id: string, scenario?: Scenario): Promise<void>
  remove(id: string, scenario?: Scenario): Promise<void>
  bulkUpdate(ids: string[], patch: BulkPatch, scenario?: Scenario): Promise<number>
  bulkDelete(ids: string[], scenario?: Scenario): Promise<number>
  requests(query: RequestQuery, scenario?: Scenario): Promise<RequestPage>
}
