import { describe, it, expect } from 'vitest'
import { validateApiRequest, reqIdOf, API_MESSAGE } from './appBridge'

const q = ['query']
const qi = ['query', 'ingest']

describe('validateApiRequest', () => {
  it('requires the dedicated publication scope only at the notification endpoint', () => {
    const request = { type: API_MESSAGE, method: 'POST', path: '/api/v1/notifications', body: { occurrence_id: 'one' } }
    expect(validateApiRequest(request, ['notify']).ok).toBe(true)
    expect(validateApiRequest(request, ['ingest']).ok).toBe(false)
    expect(validateApiRequest({ ...request, method: 'DELETE' }, ['notify']).ok).toBe(false)
    expect(validateApiRequest({ ...request, path: '/api/v1/reports' }, ['notify']).ok).toBe(false)
    expect(validateApiRequest({ ...request, path: '/api/admin/notifications' }, ['notify']).ok).toBe(false)
  })
  it('accepts a GET under /api/v1/ with the query scope', () => {
    const r = validateApiRequest({ type: API_MESSAGE, reqId: 1, path: '/api/v1/symbols?q=abc' }, q)
    expect(r).toEqual({ ok: true, method: 'GET', path: '/api/v1/symbols?q=abc', body: undefined })
  })

  it('defaults the method to GET', () => {
    const r = validateApiRequest({ type: API_MESSAGE, reqId: 1, method: 'get', path: '/api/v1/runs' }, q)
    expect(r.ok).toBe(true)
  })

  it('refuses writes without the ingest scope (query alone is read-only)', () => {
    for (const method of ['POST', 'DELETE']) {
      const r = validateApiRequest({ type: API_MESSAGE, reqId: 1, method, path: '/api/v1/reports' }, q)
      expect(r.ok).toBe(false)
    }
  })

  it('allows POST/DELETE under /api/v1/ with the ingest scope, passing the body', () => {
    const r = validateApiRequest({ type: API_MESSAGE, reqId: 1, method: 'POST', path: '/api/v1/reports', body: { a: 1 } }, qi)
    expect(r).toEqual({ ok: true, method: 'POST', path: '/api/v1/reports', body: { a: 1 } })
    const d = validateApiRequest({ type: API_MESSAGE, reqId: 1, method: 'DELETE', path: '/api/v1/reports/5' }, qi)
    expect(d.ok).toBe(true)
  })

  it('refuses PATCH/PUT even with the ingest scope (write surface is POST/DELETE only)', () => {
    for (const method of ['PATCH', 'PUT']) {
      const r = validateApiRequest({ type: API_MESSAGE, reqId: 1, method, path: '/api/v1/reports' }, qi)
      expect(r.ok).toBe(false)
    }
  })

  it('refuses paths outside /api/v1/', () => {
    for (const path of ['/api/admin/users', '/etc/passwd', 'https://evil.com/api/v1/x', '//evil.com/x', '/api/login']) {
      const r = validateApiRequest({ type: API_MESSAGE, reqId: 1, path }, q)
      expect(r.ok).toBe(false)
    }
  })

  it('refuses traversal and malformed paths', () => {
    for (const path of [
      '/api/v1/../admin',
      '/api/v1/%2e%2e/admin/tokens',
      '/api/v1/.%2E/admin/tokens',
      '/api/v1/%252e%252e/admin/tokens',
      '/api/v1//x',
      '/api/v1/a b',
    ]) {
      expect(validateApiRequest({ type: API_MESSAGE, reqId: 1, path }, q).ok).toBe(false)
    }
  })

  it('refuses when the app lacks the query scope', () => {
    expect(validateApiRequest({ type: API_MESSAGE, reqId: 1, path: '/api/v1/symbols' }, []).ok).toBe(false)
  })

  it('refuses non-bridge messages', () => {
    expect(validateApiRequest({ type: 'other', path: '/api/v1/x' }, q).ok).toBe(false)
    expect(validateApiRequest(null, q).ok).toBe(false)
    expect(validateApiRequest('hi', q).ok).toBe(false)
    expect(validateApiRequest({ type: API_MESSAGE, path: 123 }, q).ok).toBe(false)
  })
})

describe('reqIdOf', () => {
  it('extracts string or number ids, else null', () => {
    expect(reqIdOf({ reqId: 'a' })).toBe('a')
    expect(reqIdOf({ reqId: 7 })).toBe(7)
    expect(reqIdOf({ reqId: {} })).toBeNull()
    expect(reqIdOf(null)).toBeNull()
  })
})

describe('extension bridge capabilities', () => {
  const request = { type: API_MESSAGE, method: 'POST', path: '/api/v1/extensions/status/request', body: { method: 'GET', path: '/v1/components?limit=20' } }
  it('requires a separate read capability for an inner GET', () => {
    expect(validateApiRequest(request, ['extension.read']).ok).toBe(true)
    for (const scope of ['query', 'ingest', 'notify', 'extension.manage']) expect(validateApiRequest(request, [scope]).ok).toBe(false)
  })
  it('requires management capability for inner writes', () => {
    for (const method of ['POST', 'PUT', 'DELETE']) {
      const write = { ...request, body: { method, path: '/v1/components', body: { label: 'Example' } } }
      expect(validateApiRequest(write, ['extension.manage']).ok).toBe(true)
      expect(validateApiRequest(write, ['extension.read']).ok).toBe(false)
    }
  })
  it('rejects malformed endpoints, host overrides and traversal', () => {
    for (const path of ['/api/v1/extensions/status/request?x=1', '/api/v1/extensions/status/other', '/api/v1/extensions/status/request/extra']) expect(validateApiRequest({ ...request, path }, ['ingest', 'extension.read']).ok).toBe(false)
    for (const path of ['https://evil/v1/components', '/v1/%252e%252e/secrets', '/v1//components', '/v1/components#x', '/v1/handshake']) expect(validateApiRequest({ ...request, body: { method: 'GET', path } }, ['extension.read']).ok).toBe(false)
    expect(validateApiRequest({ ...request, method: 'GET' }, ['extension.read']).ok).toBe(false)
    expect(validateApiRequest({ ...request, body: { ...request.body, headers: {} } }, ['extension.read']).ok).toBe(false)
  })
})
