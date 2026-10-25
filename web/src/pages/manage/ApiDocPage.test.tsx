import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ApiDocPage from './ApiDocPage'

const fetchDoc = vi.hoisted(() => vi.fn())
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('./openapiDoc', () => ({ specToEndpoints: () => ({ conventions: 'Authentication guide', endpoints: [
  { method: 'GET', path: '/api/v1/reports', summary: 'Find reports', scope: 'query', params: [], errors: [], response: '' },
  { method: 'POST', path: '/api/v1/reports', summary: 'Ingest reports', scope: 'ingest', params: [], errors: [], response: '' },
] }) }))
beforeEach(() => { vi.stubGlobal('fetch', fetchDoc); fetchDoc.mockReset().mockResolvedValue({ ok: true, json: async () => ({}) }) })
describe('API reference navigation', () => {
  it('finds endpoints by method, path and description without hiding the conventions', async () => {
    render(<ApiDocPage />)
    await screen.findByText('Ingest reports')
    fireEvent.change(screen.getByLabelText('manage.apiSearch'), { target: { value: 'post reports' } })
    expect(screen.queryByText('Find reports')).toBeNull()
    expect(screen.getByText('Ingest reports')).toBeTruthy()
    expect(screen.getByText('Authentication guide')).toBeTruthy()
    fireEvent.change(screen.getByLabelText('manage.apiSearch'), { target: { value: 'unknown' } })
    expect(screen.getByText('manage.noResults')).toBeTruthy()
  })
  it('offers a retry when the API reference request fails', async () => {
    fetchDoc.mockRejectedValueOnce(new Error('Offline'))
    render(<ApiDocPage />)
    fireEvent.click(await screen.findByRole('button', { name: /common.retry/ }))
    await waitFor(() => expect(screen.getByText('Ingest reports')).toBeTruthy())
  })
})
