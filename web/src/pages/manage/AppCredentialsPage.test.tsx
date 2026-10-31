import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter, Route, Routes } from 'react-router'
import AppCredentialsPage from './AppCredentialsPage'
import { installed, layout } from './appsAdminFixtures'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('../../api/client', () => ({
  api: apiMock,
  errText: () => 'Request failed',
  ApiError: class extends Error {},
}))

vi.mock('react-i18next', () => {
  const t = (key: string, options?: Record<string, unknown>) =>
    options === undefined ? key : `${key}:${JSON.stringify(options)}`
  return { useTranslation: () => ({ t, i18n: { language: 'en-US' } }) }
})

const credential = {
  id: '00000000-0000-4000-8000-000000000101',
  label: 'Upstream API',
  state: 'active' as const,
  value_revision: 1,
  revision: 1,
  created_ms: 1,
  updated_ms: 1,
}

const binding = {
  id: '00000000-0000-4000-8000-000000000102',
  name: 'upstream-api',
  credential_id: credential.id,
  credential_label: credential.label,
  credential_value_revision: 1,
  operation: 'http_bearer' as const,
  state: 'active' as const,
  policy_revision: 1,
  revision: 1,
}

function renderPage() {
  return render(
    <App>
      <MemoryRouter initialEntries={['/manage/apps/hello/credentials']}>
        <Routes>
          <Route path="/manage/apps/:key/credentials" element={<AppCredentialsPage />} />
        </Routes>
      </MemoryRouter>
    </App>,
  )
}

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.post.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/apps/layout') return { apps: layout() }
    if (url === '/api/apps') return { apps: installed }
    if (url.startsWith('/api/admin/app-platform/credentials?'))
      return { credentials: [credential], next_after: '' }
    if (url === '/api/admin/app-platform/apps/hello/secret-bindings')
      return { bindings: [binding] }
    throw new Error(`unexpected ${url}`)
  })
})

describe('application credential settings', () => {
  it('creates a credential once without displaying the value', async () => {
    const user = userEvent.setup()
    apiMock.post.mockResolvedValue({ ...credential, id: '00000000-0000-4000-8000-000000000103' })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.createCredential/ }))
    await user.type(screen.getByLabelText('appCredentials.label'), 'Second upstream')
    await user.type(screen.getByLabelText('appCredentials.value'), 'disposable-secret')
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/credentials', {
        label: 'Second upstream',
        value_base64: btoa('disposable-secret'),
      }),
    )
    expect(document.body.textContent).not.toContain(btoa('disposable-secret'))
  })

  it('reviews and approves a named binding', async () => {
    const user = userEvent.setup()
    const planID = '00000000-0000-4000-8000-000000000104'
    apiMock.post
      .mockResolvedValueOnce({
        installation_id: '00000000-0000-4000-8000-000000000105',
        plan_id: planID,
        plan_sha256: 'a'.repeat(64),
        review: {
          name: 'second-api',
          credential_id: credential.id,
          credential_label: credential.label,
          operation: 'http_bearer',
          network_policy_sha256: 'b'.repeat(64),
        },
      })
      .mockResolvedValueOnce({ ...binding, name: 'second-api' })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    await user.type(screen.getByLabelText('appCredentials.name'), 'second-api')
    await user.click(screen.getByLabelText('appCredentials.credential'))
    fireEvent.click(await screen.findByText('Upstream API', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByRole('button', { name: 'appCredentials.reviewBinding' }))
    expect(await screen.findAllByText('appCredentials.confirmBinding')).toHaveLength(2)
    await user.click(screen.getByRole('button', { name: 'appCredentials.approve' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenLastCalledWith(
        `/api/admin/app-platform/secret-binding-reviews/${planID}`,
        { plan_id: planID, plan_sha256: 'a'.repeat(64) },
      ),
    )
  })
})
