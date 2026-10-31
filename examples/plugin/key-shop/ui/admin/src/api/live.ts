import { ttlSecondsFromLocal } from '@/lib/datetime'
import { ApiError } from '@/lib/api-error'
import { bulkPayload } from '@/lib/bulk'
import type { AdminService, BulkPatch, CreateDraft, CreatedTunnel, EditDraft, GroupInput, KeysQuery, Partner, PartnerCall, PartnerCallQuery, PartnerInput, PartnerOrder, PartnerOrderQuery, PartnerSettings, PartnerStats, Plan, PlanInput, Policy, RequestQuery, Scenario, Tunnel, TunnelDraft, UsageQuery } from '@/types'
import { managementJSON, managementRequest } from './http'
import { mapDiscoveredModels, mapGroup, mapKey, mapPolicyModels, mapRequest, mapStats, mapSummary, mapTunnel, mapUsage } from './map'
import { asRecord, pickNumber, pickString } from './pick'

const ROOT = '/v0/management/plugins/key-shop'
const KEYS_PATH = `${ROOT}/keys`
const POLICY_PATH = '/v8/management/config/plugins/configs/key-shop'
const ACCESS_KEYS_PATH = '/v8/management/config/access/api-keys'
const PAGE_SIZE = 25

function mapCreatedTunnel(raw: unknown): CreatedTunnel | null {
  const row = asRecord(raw)
  if (!row || (!pickString(row, 'id') && !pickString(row, 'error') && !pickString(row, 'endpoint'))) return null
  return {
    id: pickString(row, 'id'),
    name: pickString(row, 'name'),
    status: pickString(row, 'status'),
    publicUrl: pickString(row, 'public_url'),
    endpoint: pickString(row, 'endpoint'),
    error: pickString(row, 'error'),
  }
}

// mapTunnelSave keeps the create-path start_error next to the mapped tunnel.
function mapTunnelSave(raw: unknown): Tunnel {
  const tunnel = mapTunnel(raw)
  const startError = pickString(asRecord(raw), 'start_error')
  return startError ? { ...tunnel, startError } : tunnel
}

function queryString(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value == null || value === '') continue
    search.set(key, String(value))
  }
  const text = search.toString()
  return text ? `?${text}` : ''
}

function optionalNumber(row: Record<string, unknown> | null, key: string): number | null {
  const value = row?.[key]
  return typeof value === 'number' ? value : null
}

function mapPlan(raw: unknown): Plan {
  const row = asRecord(raw)
  const group = asRecord(row?.group)
  return {
    id: pickString(row, 'id'),
    name: pickString(row, 'name'),
    description: pickString(row, 'description'),
    revision: pickNumber(row, 'revision'),
    active: Boolean(row?.active),
    group: {
      id: pickString(group, 'id'),
      name: pickString(group, 'name'),
      allModels: Boolean(group?.all_models),
      models: Array.isArray(group?.models) ? (group.models as unknown[]).map((item) => String(item)).filter(Boolean) : [],
    },
    ttlSeconds: pickNumber(row, 'ttl_seconds'),
    neverExpires: Boolean(row?.never_expires),
    tokenLimit: pickNumber(row, 'token_limit'),
    unlimitedTokens: Boolean(row?.unlimited_tokens),
    requestLimit: pickNumber(row, 'request_limit'),
    unlimitedRequests: Boolean(row?.unlimited_requests),
  }
}

function mapPartnerStats(raw: unknown): PartnerStats {
  const row = asRecord(raw)
  return {
    callsOk: pickNumber(row, 'calls_ok'),
    callsClientError: pickNumber(row, 'calls_client_error'),
    callsServerError: pickNumber(row, 'calls_server_error'),
    minted: pickNumber(row, 'minted'),
    confirmed: pickNumber(row, 'confirmed'),
    voided: pickNumber(row, 'voided'),
    unconfirmedOverdue: pickNumber(row, 'unconfirmed_overdue'),
    dailyUsed: pickNumber(row, 'daily_used'),
    dailyCap: pickNumber(row, 'daily_cap'),
  }
}

function mapPartner(raw: unknown): Partner {
  const row = asRecord(raw)
  const stats = asRecord(row?.stats)
  const status = pickString(row, 'status') === 'disabled' ? 'disabled' : 'active'
  return {
    id: pickString(row, 'id'),
    name: pickString(row, 'name'),
    secretPrefix: pickString(row, 'secret_prefix'),
    secretLast4: pickString(row, 'secret_last4'),
    status,
    allowedPlanIds: Array.isArray(row?.allowed_plan_ids) ? (row.allowed_plan_ids as unknown[]).map((item) => String(item)).filter(Boolean) : [],
    dailyKeyCap: optionalNumber(row, 'daily_key_cap'),
    maxQuantity: optionalNumber(row, 'max_quantity'),
    createdAt: pickString(row, 'created_at'),
    updatedAt: pickString(row, 'updated_at'),
    lastUsedAt: pickString(row, 'last_used_at') || null,
    stats: stats ? mapPartnerStats(stats) : undefined,
  }
}

function mapPartnerCall(raw: unknown): PartnerCall {
  const row = asRecord(raw)
  return {
    id: pickNumber(row, 'id'),
    partnerId: pickString(row, 'partner_id'),
    at: pickString(row, 'at'),
    op: pickString(row, 'op'),
    httpStatus: pickNumber(row, 'http_status'),
    code: pickString(row, 'code'),
    ref: pickString(row, 'ref'),
    latencyMs: pickNumber(row, 'latency_ms'),
    clientIp: pickString(row, 'client_ip'),
  }
}

function mapPartnerOrder(raw: unknown): PartnerOrder {
  const row = asRecord(raw)
  const state = pickString(row, 'state')
  return {
    partnerId: pickString(row, 'partner_id'),
    ref: pickString(row, 'ref'),
    planId: pickString(row, 'plan_id'),
    planRevision: pickNumber(row, 'plan_revision'),
    quantity: pickNumber(row, 'quantity'),
    state: state === 'confirmed' || state === 'voided' ? state : 'minted',
    orderId: pickString(row, 'order_id'),
    buyer: pickString(row, 'buyer'),
    customer: pickString(row, 'customer'),
    voidReason: pickString(row, 'void_reason'),
    createdAt: pickString(row, 'created_at'),
    confirmedAt: pickString(row, 'confirmed_at') || null,
    voidedAt: pickString(row, 'voided_at') || null,
  }
}

function mapPartnerSettings(raw: unknown): PartnerSettings {
  const row = asRecord(raw)
  return {
    apiEnabled: Boolean(row?.partner_api_enabled),
    publicBaseUrl: pickString(row, 'public_base_url'),
    publicDashboardPath: pickString(row, 'public_dashboard_path'),
    maxQuantity: optionalNumber(row, 'partner_max_quantity'),
    dailyKeyCap: optionalNumber(row, 'partner_daily_key_cap'),
    maxBodyKb: optionalNumber(row, 'partner_max_body_kb'),
    alertMin: optionalNumber(row, 'partner_unconfirmed_alert_min'),
    logRetentionDays: optionalNumber(row, 'partner_log_retention_days'),
    labelTemplate: pickString(row, 'partner_key_label_template'),
    noteTemplate: pickString(row, 'partner_key_note_template'),
  }
}

function setOptionalNumber(payload: Record<string, unknown>, key: string, value: number | null) {
  if (value == null) delete payload[key]
  else payload[key] = value
}

function setOptionalString(payload: Record<string, unknown>, key: string, value: string) {
  const trimmed = value.trim()
  if (!trimmed) delete payload[key]
  else payload[key] = trimmed
}

function applyPartnerSettings(payload: Record<string, unknown>, next: PartnerSettings) {
  payload.partner_api_enabled = next.apiEnabled
  setOptionalString(payload, 'public_base_url', next.publicBaseUrl)
  setOptionalString(payload, 'public_dashboard_path', next.publicDashboardPath)
  setOptionalNumber(payload, 'partner_max_quantity', next.maxQuantity)
  setOptionalNumber(payload, 'partner_daily_key_cap', next.dailyKeyCap)
  setOptionalNumber(payload, 'partner_max_body_kb', next.maxBodyKb)
  setOptionalNumber(payload, 'partner_unconfirmed_alert_min', next.alertMin)
  setOptionalNumber(payload, 'partner_log_retention_days', next.logRetentionDays)
  setOptionalString(payload, 'partner_key_label_template', next.labelTemplate)
  setOptionalString(payload, 'partner_key_note_template', next.noteTemplate)
}

function partnerBody(input: PartnerInput, forUpdate: boolean): Record<string, unknown> {
  return {
    ...(forUpdate && input.id ? { id: input.id } : {}),
    name: input.name.trim(),
    ...(forUpdate ? { status: input.status || 'active' } : {}),
    allowed_plan_ids: input.allowedPlanIds,
    daily_key_cap: input.dailyKeyCap,
    max_quantity: input.maxQuantity,
  }
}

export function createLiveAdminService(): AdminService {
  let secret = ''
  let discovered: string[] | null = null
  let loadedConfig: Record<string, unknown> = {}
  let policyLoaded = false

  async function call(method: string, path: string, body?: unknown, timeoutMs?: number) {
    return managementRequest(secret, method, path, body, timeoutMs)
  }

  function savedAccessKeys(payload: unknown): string[] {
    if (!Array.isArray(payload)) return []
    const seen = new Set<string>()
    const keys: string[] = []
    for (const item of payload) {
      const record = asRecord(item)
      const value = typeof item === 'string' ? item : pickString(record, 'api-key', 'apiKey', 'key', 'Key')
      const key = value.trim()
      if (!key || seen.has(key)) continue
      seen.add(key)
      keys.push(key)
    }
    return keys
  }

  async function catalogModels(): Promise<string[]> {
    if (discovered) return discovered
    let keys: string[] = []
    try {
      keys = savedAccessKeys(await managementJSON(secret, 'GET', ACCESS_KEYS_PATH))
    } catch {
      keys = []
    }
    for (const key of keys) {
      try {
        const response = await fetch('/v1/models', {
          headers: { Authorization: `Bearer ${key}` },
          signal: AbortSignal.timeout(8000),
        })
        if (!response.ok) continue
        const models = mapDiscoveredModels(await response.json())
        if (models.length) {
          discovered = models
          return discovered
        }
      } catch {
        // try the next saved operator key
      }
    }
    const fallback = ['codex', 'gemini', 'claude', 'xai', 'meta', 'gemini-interactions']
    const responses = await Promise.all(fallback.map(async (channel) => {
      try {
        return await call('GET', `/v8/management/routing/model-definitions/${channel}`)
      } catch {
        return null
      }
    }))
    const seen = new Set<string>()
    discovered = responses.flatMap((item) => mapDiscoveredModels(item)).filter((name) => {
      const key = name.toLowerCase()
      if (!key || seen.has(key)) return false
      seen.add(key)
      return true
    })
    return discovered
  }

  async function loadPolicy(): Promise<Policy> {
    try {
      const nextConfig = await call('GET', POLICY_PATH)
      loadedConfig = nextConfig
      policyLoaded = true
      return {
        mode: nextConfig.model_access_mode === 'custom' ? 'custom' : 'full',
        models: mapPolicyModels(nextConfig.models),
        loaded: true,
      }
    } catch {
      policyLoaded = false
      return { mode: 'full', models: [], loaded: false }
    }
  }

  return {
    pageSize: PAGE_SIZE,
    async authenticate(value: string) {
      secret = ''
      const next = value.trim()
      if (!next) throw new Error('Enter the management key.')
      if (next.startsWith('ck_')) {
        throw new Error('This is a customer API key (ck_...). Use management_password from ACCESS.txt to open the admin workspace.')
      }
      secret = next
      try {
        await call('GET', `${ROOT}/stats`)
      } catch (error) {
        secret = ''
        if (error instanceof ApiError && error.status === 401) {
          throw new Error('The management password is incorrect. Use management_password from ACCESS.txt, not a customer API key.')
        }
        throw error
      }
    },
    async stats(_scenario?: Scenario) {
      return mapStats(await call('GET', `${ROOT}/stats`))
    },
    async usage(query: UsageQuery, _scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/stats/usage${queryString({ from: query.from, to: query.to, bucket: query.bucket })}`)
      return mapUsage(data)
    },
    async policy(_scenario?: Scenario) {
      return loadPolicy()
    },
    async savePolicy(next: Policy, _scenario?: Scenario) {
      if (!policyLoaded) await loadPolicy()
      if (!policyLoaded) throw new Error('Model policy has not loaded.')
      const models = next.models.map((item) => ({
        name: item.name.trim(),
        ...(item.alias.trim() ? { alias: item.alias.trim() } : {}),
      })).filter((item) => item.name)
      const payload = { ...loadedConfig, model_access_mode: next.mode, models }
      await call('PUT', POLICY_PATH, payload)
      loadedConfig = payload
    },
    async groups(_scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/groups`)
      return Array.isArray(data.groups) ? data.groups.map(mapGroup) : []
    },
    async saveGroup(input: GroupInput, _scenario?: Scenario) {
      const body = {
        ...(input.id ? { id: input.id } : {}),
        name: input.name.trim(),
        description: input.description.trim(),
        all_models: input.allModels,
        models: input.models.map((item) => ({
          name: item.name.trim(),
          alias: item.alias.trim(),
        })).filter((item) => item.name),
      }
      const data = await call('POST', input.id ? `${ROOT}/groups/update` : `${ROOT}/groups`, body)
      return mapGroup(data.group)
    },
    async removeGroup(id: string, _scenario?: Scenario) {
      await call('POST', `${ROOT}/groups/delete`, { id })
    },
    async catalog(_scenario?: Scenario) {
      return catalogModels()
    },
    async keys(query: KeysQuery, _scenario?: Scenario) {
      const data = await call('GET', `${KEYS_PATH}${queryString({
        q: query.q,
        status: query.status,
        group: query.group,
        partner: query.partner,
        ref: query.ref,
        page: query.page,
        page_size: query.pageSize,
      })}`)
      const keys = Array.isArray(data.keys) ? data.keys.map((item) => {
        const row = asRecord(item)
        return { ...mapKey(item), partnerId: pickString(row, 'partner_id'), partnerRef: pickString(row, 'partner_ref') }
      }) : []
      return { keys, total: pickNumber(data, 'total') || keys.length }
    },
    async create(draft: CreateDraft, _scenario?: Scenario) {
      const count = Math.min(500, Math.max(1, Number(draft.count) || 1))
      const body: Record<string, unknown> = {
        label: draft.label,
        customer: draft.customer,
        note: draft.note,
        count,
        group_id: draft.groupId,
        ttl_seconds: draft.neverExpires ? 0 : ttlSecondsFromLocal(draft.expiresLocal),
        token_limit: Number(draft.tokenLimit),
        request_limit: Number(draft.requestLimit),
        never_expires: draft.neverExpires,
        unlimited_tokens: draft.unlimitedTokens,
        unlimited_requests: draft.unlimitedRequests,
        ...(draft.requestId ? { request_id: draft.requestId } : {}),
      }
      if (draft.tunnelEnabled) {
        body.tunnel = { enabled: true, ...(draft.tunnelTarget?.trim() ? { target_url: draft.tunnelTarget.trim() } : {}) }
      }
      const data = await call('POST', count === 1 ? KEYS_PATH : `${KEYS_PATH}/bulk`, body, 30000)
      const secrets = typeof data.secret === 'string' && data.secret
        ? [data.secret]
        : Array.isArray(data.keys) ? data.keys.map((item) => pickString(asRecord(item), 'Secret', 'secret')).filter(Boolean) : []
      return { secrets, tunnel: mapCreatedTunnel(data.tunnel) }
    },
    async update(draft: EditDraft, _scenario?: Scenario) {
      await call('POST', `${KEYS_PATH}/update`, {
        id: draft.id,
        label: draft.label,
        customer: draft.customer,
        group_id: draft.groupId,
        token_limit: Number(draft.tokenLimit),
        request_limit: draft.unlimitedRequests ? 0 : Number(draft.requestLimit),
        unlimited_requests: draft.unlimitedRequests,
        status: draft.status,
        ...(draft.neverExpires ? { never_expires: true } : { ttl_seconds: ttlSecondsFromLocal(draft.expiresLocal) }),
      })
    },
    async setEnabled(id: string, enabled: boolean, _scenario?: Scenario) {
      await call('POST', `${KEYS_PATH}/update`, { id, status: enabled ? 'active' : 'disabled' })
    },
    async rotate(id: string, _scenario?: Scenario): Promise<string> {
      const data = await call('POST', `${KEYS_PATH}/rotate`, { id })
      return pickString(data, 'secret', 'Secret')
    },
    async resetUsage(id: string, _scenario?: Scenario) {
      await call('POST', `${KEYS_PATH}/reset-usage`, { id })
    },
    async remove(id: string, _scenario?: Scenario) {
      await call('POST', `${KEYS_PATH}/delete`, { id })
    },
    async bulkUpdate(ids: string[], patch: BulkPatch, _scenario?: Scenario) {
      const data = await call('POST', `${KEYS_PATH}/bulk-update`, bulkPayload(ids, patch))
      return pickNumber(data, 'updated')
    },
    async bulkDelete(ids: string[], _scenario?: Scenario) {
      const data = await call('POST', `${KEYS_PATH}/bulk-delete`, { ids })
      return pickNumber(data, 'deleted')
    },
    async requests(query: RequestQuery, _scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/requests${queryString({
        id: query.id,
        customer: query.customer,
        group: query.group,
        status: query.status,
        model: query.model,
        account: query.account,
        effort: query.effort,
        code: query.code,
        q: query.q,
        from: query.from,
        to: query.to,
        limit: query.limit,
        cursor: query.cursor,
      })}`)
      return {
        requests: Array.isArray(data.requests) ? data.requests.map(mapRequest) : [],
        nextCursor: pickString(data, 'next_cursor'),
        hasMore: Boolean(data.has_more),
        total: pickNumber(data, 'total'),
        summary: mapSummary(data.summary),
      }
    },
    async tunnels(_scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/tunnels`)
      const info = asRecord(data.cloudflared)
      return {
        tunnels: Array.isArray(data.tunnels) ? data.tunnels.map(mapTunnel) : [],
        cloudflared: {
          found: Boolean(info.found),
          path: pickString(info, 'path'),
          version: pickString(info, 'version'),
        },
        defaultTargetUrl: pickString(data, 'default_target_url'),
      }
    },
    async saveTunnel(draft: TunnelDraft, _scenario?: Scenario) {
      const token = draft.token.trim()
      if (draft.id) {
        const body: Record<string, unknown> = {
          id: draft.id,
          name: draft.name.trim(),
          target_url: draft.targetUrl.trim(),
          autostart: draft.autostart,
        }
        if (token) body.token = token
        return mapTunnel(await call('POST', `${ROOT}/tunnels/update`, body))
      }
      return mapTunnelSave(await call('POST', `${ROOT}/tunnels`, {
        name: draft.name.trim(),
        kind: draft.kind,
        target_url: draft.targetUrl.trim(),
        token,
        autostart: draft.autostart,
      }))
    },
    async removeTunnel(id: string, _scenario?: Scenario) {
      await call('POST', `${ROOT}/tunnels/delete`, { id })
    },
    async startTunnel(id: string, _scenario?: Scenario) {
      return mapTunnel(await call('POST', `${ROOT}/tunnels/start`, { id }))
    },
    async stopTunnel(id: string, _scenario?: Scenario) {
      return mapTunnel(await call('POST', `${ROOT}/tunnels/stop`, { id }))
    },
    async tunnelLogs(id: string, lines = 200, _scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/tunnels/logs${queryString({ id, lines })}`)
      return Array.isArray(data.lines) ? data.lines.map((line) => String(line)) : []
    },
    async plans(_scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/plans`)
      return Array.isArray(data.plans) ? data.plans.map(mapPlan) : []
    },
    async savePlan(input: PlanInput, _scenario?: Scenario) {
      const body = {
        ...(input.id ? { id: input.id } : {}),
        name: input.name.trim(),
        description: input.description.trim(),
        group_id: input.groupId,
        ttl_seconds: input.unlimitedTokens ? 0 : Number(input.ttlSeconds) || 0,
        never_expires: input.neverExpires,
        token_limit: input.unlimitedTokens ? 0 : Number(input.tokenLimit) || 0,
        unlimited_tokens: input.unlimitedTokens,
        request_limit: input.unlimitedRequests ? 0 : Number(input.requestLimit) || 0,
        unlimited_requests: input.unlimitedRequests,
        active: input.active,
      }
      body.ttl_seconds = input.neverExpires ? 0 : Number(input.ttlSeconds) || 0
      const data = await call('POST', input.id ? `${ROOT}/plans/update` : `${ROOT}/plans`, body)
      return mapPlan(data.plan)
    },
    async removePlan(id: string, _scenario?: Scenario) {
      await call('POST', `${ROOT}/plans/delete`, { id })
    },
    async partners(_scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/partners`)
      return Array.isArray(data.partners) ? data.partners.map(mapPartner) : []
    },
    async createPartner(input: PartnerInput, _scenario?: Scenario) {
      const data = await call('POST', `${ROOT}/partners`, partnerBody(input, false))
      return { partner: mapPartner(data.partner), secret: pickString(data, 'secret') }
    },
    async updatePartner(input: PartnerInput, _scenario?: Scenario) {
      const data = await call('POST', `${ROOT}/partners/update`, partnerBody(input, true))
      return mapPartner(data.partner)
    },
    async rotatePartner(id: string, _scenario?: Scenario) {
      const data = await call('POST', `${ROOT}/partners/rotate`, { id })
      return { partner: mapPartner(data.partner), secret: pickString(data, 'secret') }
    },
    async removePartner(id: string, _scenario?: Scenario) {
      await call('POST', `${ROOT}/partners/delete`, { id })
    },
    async partnerCalls(query: PartnerCallQuery, _scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/partners/calls${queryString({
        partner: query.partner,
        op: query.op,
        status: query.status,
        from: query.from,
        to: query.to,
        limit: query.limit,
        cursor: query.cursor,
      })}`)
      return {
        calls: Array.isArray(data.calls) ? data.calls.map(mapPartnerCall) : [],
        nextCursor: pickString(data, 'next_cursor'),
      }
    },
    async partnerOrders(query: PartnerOrderQuery, _scenario?: Scenario) {
      const data = await call('GET', `${ROOT}/partners/orders${queryString({
        partner: query.partner,
        state: query.state,
        ref: query.ref,
        from: query.from,
        to: query.to,
        limit: query.limit,
        cursor: query.cursor,
      })}`)
      return {
        orders: Array.isArray(data.orders) ? data.orders.map(mapPartnerOrder) : [],
        nextCursor: pickString(data, 'next_cursor'),
      }
    },
    async partnerSettings(_scenario?: Scenario) {
      const config = await call('GET', POLICY_PATH)
      loadedConfig = config
      policyLoaded = true
      return mapPartnerSettings(config)
    },
    async savePartnerSettings(next: PartnerSettings, _scenario?: Scenario) {
      if (!policyLoaded) {
        loadedConfig = await call('GET', POLICY_PATH)
        policyLoaded = true
      }
      const payload = { ...loadedConfig }
      applyPartnerSettings(payload, next)
      await call('PUT', POLICY_PATH, payload)
      loadedConfig = payload
    },
  }
}

export type LiveAdminService = AdminService
