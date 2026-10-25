import { afterEach, expect, it, vi } from 'vitest'
import { api, ApiError, errText } from './client'
afterEach(() => vi.unstubAllGlobals())
it('classifies transport failures separately from invalid credentials', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
  const error = await api.post('/api/login', {}).catch(e => e)
  expect(error).toBeInstanceOf(ApiError)
  expect(error.code).toBe('network_unavailable')
  expect(errText(error, k => `translated:${k}`, 'login.error')).toBe('translated:err.network_unavailable')
})
it('classifies proxy failures and unexpected HTML instead of accepting a false success', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('<html>Offline</html>', { status: 200 })))
  await expect(api.post('/api/password/forgot', {})).rejects.toMatchObject({ code: 'server_unavailable' })
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('Bad Gateway', { status: 502 })))
  await expect(api.get('/api/me')).rejects.toMatchObject({ code: 'server_unavailable' })
})
