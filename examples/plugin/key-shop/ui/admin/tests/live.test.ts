import { afterEach, expect, mock, test } from 'bun:test'
import { createLiveAdminService } from '../src/api/live'

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
})

test('customer keys are rejected locally without spending a server login attempt', async () => {
  const fetch = mock((_input: string | URL | Request, _init?: RequestInit) => Promise.resolve(new Response('{}')))
  globalThis.fetch = fetch as unknown as typeof globalThis.fetch
  await expect(createLiveAdminService().authenticate(' ck_customer_example ')).rejects.toThrow('customer API key')
  expect(fetch).not.toHaveBeenCalled()
})

test('temporary IP bans retain the server reason and remaining time', async () => {
  globalThis.fetch = mock(() => Promise.resolve(new Response(JSON.stringify({
    error: 'IP banned due to too many failed attempts. Try again in 27m53s',
  }), { status: 403 }))) as unknown as typeof globalThis.fetch
  await expect(createLiveAdminService().authenticate('management-example')).rejects.toThrow('27m53s')
})

test('server errors do not masquerade as an incorrect management password', async () => {
  globalThis.fetch = mock(() => Promise.resolve(new Response(JSON.stringify({
    error: 'Key store unavailable',
  }), { status: 503 }))) as unknown as typeof globalThis.fetch
  await expect(createLiveAdminService().authenticate('management-example')).rejects.toThrow('Key store unavailable')
})

test('invalid passwords explain which credential is required', async () => {
  globalThis.fetch = mock(() => Promise.resolve(new Response(JSON.stringify({
    error: 'invalid management key',
  }), { status: 401 }))) as unknown as typeof globalThis.fetch
  await expect(createLiveAdminService().authenticate('wrong-example')).rejects.toThrow('management_password from ACCESS.txt')
})

test('valid passwords are trimmed and sent to the authenticated management route', async () => {
  const fetch = mock((_input: string | URL | Request, _init?: RequestInit) => Promise.resolve(new Response('{}')))
  globalThis.fetch = fetch as unknown as typeof globalThis.fetch
  await createLiveAdminService().authenticate(' management-example ')
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(fetch.mock.calls[0]?.[0]).toBe('/v0/management/plugins/key-shop/stats')
  const options = fetch.mock.calls[0]?.[1] as RequestInit
  expect((options.headers as Record<string, string>).Authorization).toBe('Bearer management-example')
})
