import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App, ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
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
  principals: ['viewer'] as ('viewer' | 'service')[],
}

function renderPage(algorithm = theme.defaultAlgorithm, appKey = 'hello') {
  return render(
    <ConfigProvider theme={{ algorithm }}>
      <App>
        <MemoryRouter initialEntries={[`/manage/apps/${appKey}/credentials`]}>
          <Routes>
            <Route path="/manage/apps/:key/credentials" element={<AppCredentialsPage />} />
          </Routes>
        </MemoryRouter>
      </App>
    </ConfigProvider>,
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
          network_target: 'https://api.example.com',
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
    expect(screen.getByText('appCredentials.bindingTarget:{"origin":"https://api.example.com"}')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appCredentials.approve' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenLastCalledWith(
        `/api/admin/app-platform/secret-binding-reviews/${planID}`,
        { plan_id: planID, plan_sha256: 'a'.repeat(64) },
      ),
    )
  })

  it('says who may use each binding', async () => {
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      if (url === '/api/apps') return { apps: installed }
      if (url.startsWith('/api/admin/app-platform/credentials?'))
        return { credentials: [credential], next_after: '' }
      if (url === '/api/admin/app-platform/apps/hello/secret-bindings')
        return {
          bindings: [
            binding,
            { ...binding, id: '00000000-0000-4000-8000-000000000112', name: 'both-api', principals: ['service', 'viewer'] },
            { ...binding, id: '00000000-0000-4000-8000-000000000113', name: 'service-api', principals: ['service'] },
          ],
        }
      throw new Error(`unexpected ${url}`)
    })
    renderPage()
    await screen.findAllByText('Upstream API')
    expect(screen.getByText('appCredentials.principals.viewer')).toBeTruthy()
    expect(screen.getByText('appCredentials.principals.both')).toBeTruthy()
    expect(screen.getByText('appCredentials.principals.service')).toBeTruthy()
  })

  it('asks for background use explicitly and discloses it before approval', async () => {
    const user = userEvent.setup()
    const planID = '00000000-0000-4000-8000-000000000114'
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
          principals: ['service', 'viewer'],
        },
      })
      .mockResolvedValueOnce({ ...binding, name: 'second-api', principals: ['service', 'viewer'] })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    await user.type(screen.getByLabelText('appCredentials.name'), 'second-api')
    await user.click(screen.getByLabelText('appCredentials.credential'))
    fireEvent.click(await screen.findByText('Upstream API', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByLabelText('appCredentials.principals'))
    fireEvent.click(await screen.findByText('appCredentials.principals.both', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByRole('button', { name: 'appCredentials.reviewBinding' }))
    expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/apps/hello/secret-bindings/plan', {
      name: 'second-api',
      credential_id: credential.id,
      operation: 'http_bearer',
      principals: ['service', 'viewer'],
    })
    expect(await screen.findByText('appCredentials.backgroundDisclosure')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appCredentials.approve' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenLastCalledWith(
        `/api/admin/app-platform/secret-binding-reviews/${planID}`,
        { plan_id: planID, plan_sha256: 'a'.repeat(64) },
      ),
    )
  })

  it('shows no background disclosure for a viewer-only binding', async () => {
    const user = userEvent.setup()
    apiMock.post.mockResolvedValueOnce({
      installation_id: '00000000-0000-4000-8000-000000000105',
      plan_id: '00000000-0000-4000-8000-000000000115',
      plan_sha256: 'a'.repeat(64),
      review: { name: 'second-api', credential_id: credential.id, credential_label: credential.label, operation: 'http_bearer', network_policy_sha256: 'b'.repeat(64), principals: ['viewer'] },
    })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    await user.type(screen.getByLabelText('appCredentials.name'), 'second-api')
    await user.click(screen.getByLabelText('appCredentials.credential'))
    fireEvent.click(await screen.findByText('Upstream API', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByRole('button', { name: 'appCredentials.reviewBinding' }))
    await screen.findAllByText('appCredentials.confirmBinding')
    // The default is the viewer only: background use is never preselected.
    expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/apps/hello/secret-bindings/plan', {
      name: 'second-api',
      credential_id: credential.id,
      operation: 'http_bearer',
      principals: ['viewer'],
    })
    expect(screen.queryByText('appCredentials.backgroundDisclosure')).toBeNull()
  })

  it('shows the target each binding is for', async () => {
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      if (url === '/api/apps') return { apps: installed }
      if (url.startsWith('/api/admin/app-platform/credentials?'))
        return { credentials: [credential], next_after: '' }
      if (url === '/api/admin/app-platform/apps/hello/secret-bindings')
        return { bindings: [{ ...binding, network_target: 'https://api.example.com', network_target_sha256: 'c'.repeat(64) }] }
      throw new Error(`unexpected ${url}`)
    })
    renderPage()
    await screen.findAllByText('Upstream API')
    expect(screen.getByText('https://api.example.com')).toBeTruthy()
  })

  it('asks for the target when the grant has several and sends it', async () => {
    const user = userEvent.setup()
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      if (url === '/api/apps') return { apps: installed }
      if (url.startsWith('/api/admin/app-platform/credentials?'))
        return { credentials: [credential], next_after: '' }
      if (url === '/api/admin/app-platform/apps/hello/secret-bindings')
        return {
          bindings: [],
          targets: [
            { id: 'default', origin: 'https://api.example.com' },
            { id: 'other', origin: 'https://other.example.com' },
          ],
        }
      throw new Error(`unexpected ${url}`)
    })
    apiMock.post.mockResolvedValueOnce({
      installation_id: '00000000-0000-4000-8000-000000000105',
      plan_id: '00000000-0000-4000-8000-000000000116',
      plan_sha256: 'a'.repeat(64),
      review: { name: 'second-api', credential_id: credential.id, credential_label: credential.label, operation: 'http_bearer', network_policy_sha256: 'b'.repeat(64), network_target: 'https://other.example.com', principals: ['viewer'] },
    })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    await user.type(screen.getByLabelText('appCredentials.name'), 'second-api')
    await user.click(screen.getByLabelText('appCredentials.credential'))
    fireEvent.click(await screen.findByText('Upstream API', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByLabelText('appCredentials.target'))
    fireEvent.click(await screen.findByText('other · https://other.example.com', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByRole('button', { name: 'appCredentials.reviewBinding' }))
    await screen.findAllByText('appCredentials.confirmBinding')
    expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/apps/hello/secret-bindings/plan', {
      name: 'second-api',
      credential_id: credential.id,
      operation: 'http_bearer',
      principals: ['viewer'],
      target: 'other',
    })
  })

  it('offers no target choice when the grant has one', async () => {
    const user = userEvent.setup()
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      if (url === '/api/apps') return { apps: installed }
      if (url.startsWith('/api/admin/app-platform/credentials?'))
        return { credentials: [credential], next_after: '' }
      if (url === '/api/admin/app-platform/apps/hello/secret-bindings')
        return { bindings: [], targets: [{ id: 'default', origin: 'https://api.example.com' }] }
      throw new Error(`unexpected ${url}`)
    })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    expect(screen.queryByLabelText('appCredentials.target')).toBeNull()
  })

  it('lets the administrator change who may use an active binding', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: 'appCredentials.changeUse' }))
    expect((screen.getByLabelText('appCredentials.name') as HTMLInputElement).value).toBe('upstream-api')
    expect(screen.getByLabelText('appCredentials.principals')).toBeTruthy()
  })

  it('offers no background use for a setup-only credential', async () => {
    const user = userEvent.setup()
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      if (url === '/api/apps') return { apps: installed }
      if (url.startsWith('/api/admin/app-platform/credentials?'))
        return { credentials: [credential], next_after: '' }
      if (url === '/api/admin/app-platform/apps/scheduled-digest/secret-bindings')
        return { bindings: [] }
      throw new Error(`unexpected ${url}`)
    })
    renderPage(theme.defaultAlgorithm, 'scheduled-digest')
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    expect(screen.queryByLabelText('appCredentials.principals')).toBeNull()
  })

  it('prefills the required Scheduled Digest binding name', async () => {
    const user = userEvent.setup()
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      if (url === '/api/apps') return { apps: installed }
      if (url.startsWith('/api/admin/app-platform/credentials?'))
        return { credentials: [credential], next_after: '' }
      if (url === '/api/admin/app-platform/apps/scheduled-digest/secret-bindings')
        return { bindings: [] }
      throw new Error(`unexpected ${url}`)
    })
    renderPage(theme.defaultAlgorithm, 'scheduled-digest')
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    expect((screen.getByLabelText('appCredentials.name') as HTMLInputElement).value).toBe('digest-feed')
    expect(screen.getByText('appCredentials.operation.raw_read')).toBeTruthy()
  })

  it('creates an Alibaba Cloud AccessKey credential from its parts without displaying them', async () => {
    const user = userEvent.setup()
    apiMock.post.mockResolvedValue({ ...credential, id: '00000000-0000-4000-8000-000000000103' })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.createCredential/ }))
    await user.type(screen.getByLabelText('appCredentials.label'), 'Qwen billing')
    await user.click(screen.getByLabelText('appCredentials.kind'))
    fireEvent.click(await screen.findByText('appCredentials.kind.acs3', { selector: '.ant-select-item-option-content' }))
    await waitFor(() => expect(screen.queryByLabelText('appCredentials.value')).toBeNull())
    await user.type(await screen.findByLabelText('appCredentials.accessKeyId'), 'LTAIexample')
    await user.type(screen.getByLabelText('appCredentials.accessKeySecret'), 'sec"ret\\value')
    await user.type(screen.getByLabelText('appCredentials.securityToken'), 'STS.example-token')
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    const expected = JSON.stringify({ access_key_id: 'LTAIexample', access_key_secret: 'sec"ret\\value', security_token: 'STS.example-token' })
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/credentials', {
        label: 'Qwen billing',
        value_base64: btoa(expected),
      }),
    )
    expect(document.body.textContent).not.toContain('sec"ret')
    expect(document.body.textContent).not.toContain('STS.example-token')
  })

  it('leaves the security token empty when none is given', async () => {
    const user = userEvent.setup()
    apiMock.post.mockResolvedValue({ ...credential, id: '00000000-0000-4000-8000-000000000103' })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.createCredential/ }))
    await user.type(screen.getByLabelText('appCredentials.label'), 'Qwen billing')
    await user.click(screen.getByLabelText('appCredentials.kind'))
    fireEvent.click(await screen.findByText('appCredentials.kind.acs3', { selector: '.ant-select-item-option-content' }))
    await user.type(await screen.findByLabelText('appCredentials.accessKeyId'), 'LTAIexample')
    await user.type(screen.getByLabelText('appCredentials.accessKeySecret'), 'secret-value')
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/credentials', {
        label: 'Qwen billing',
        value_base64: btoa(JSON.stringify({ access_key_id: 'LTAIexample', access_key_secret: 'secret-value', security_token: '' })),
      }),
    )
  })

  it('reviews an Alibaba Cloud signed-request binding with its allowed actions, origin and RAM advice', async () => {
    const user = userEvent.setup()
    const planID = '00000000-0000-4000-8000-000000000124'
    apiMock.post
      .mockResolvedValueOnce({
        installation_id: '00000000-0000-4000-8000-000000000105',
        plan_id: planID,
        plan_sha256: 'a'.repeat(64),
        review: {
          name: 'qwen-billing',
          credential_id: credential.id,
          credential_label: credential.label,
          operation: 'acs3_hmac_sha256',
          network_origin: 'https://business.aliyuncs.com',
          network_policy_sha256: 'b'.repeat(64),
          principals: ['service'],
          acs3_actions: [
            { action: 'DescribeBill', version: '2017-12-14' },
            { action: 'QueryAccountBalance', version: '2017-12-14' },
          ],
        },
      })
      .mockResolvedValueOnce({ ...binding, name: 'qwen-billing', operation: 'acs3_hmac_sha256' })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    await user.type(screen.getByLabelText('appCredentials.name'), 'qwen-billing')
    await user.click(screen.getByLabelText('appCredentials.credential'))
    fireEvent.click(await screen.findByText('Upstream API', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByLabelText('appCredentials.operation'))
    fireEvent.click(await screen.findByText('appCredentials.operation.acs3_hmac_sha256', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByLabelText('appCredentials.principals'))
    fireEvent.click(await screen.findByText('appCredentials.principals.service', { selector: '.ant-select-item-option-content' }))
    // Out of order on purpose: the request is sorted for the canonical review.
    await user.type(screen.getByLabelText('appCredentials.acs3Action'), 'QueryAccountBalance')
    await user.type(screen.getByLabelText('appCredentials.acs3Version'), '2017-12-14')
    await user.click(screen.getByRole('button', { name: 'appCredentials.acs3AddAction' }))
    const actions = screen.getAllByLabelText('appCredentials.acs3Action')
    const versions = screen.getAllByLabelText('appCredentials.acs3Version')
    await user.type(actions[1], 'DescribeBill')
    await user.type(versions[1], '2017-12-14')
    await user.click(screen.getByRole('button', { name: 'appCredentials.reviewBinding' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/apps/hello/secret-bindings/plan', {
        name: 'qwen-billing',
        credential_id: credential.id,
        operation: 'acs3_hmac_sha256',
        principals: ['service'],
        acs3_actions: [
          { action: 'DescribeBill', version: '2017-12-14' },
          { action: 'QueryAccountBalance', version: '2017-12-14' },
        ],
      }),
    )
    expect(await screen.findByText('https://business.aliyuncs.com')).toBeTruthy()
    expect(screen.getByText('DescribeBill · 2017-12-14')).toBeTruthy()
    expect(screen.getByText('QueryAccountBalance · 2017-12-14')).toBeTruthy()
    expect(screen.getByText('appCredentials.acs3Disclosure')).toBeTruthy()
    expect(screen.getByText('appCredentials.backgroundDisclosure')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appCredentials.approve' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenLastCalledWith(
        `/api/admin/app-platform/secret-binding-reviews/${planID}`,
        { plan_id: planID, plan_sha256: 'a'.repeat(64) },
      ),
    )
  })

  it('lists the actions of a signed-request binding and reopens them for a new review', async () => {
    const user = userEvent.setup()
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      if (url === '/api/apps') return { apps: installed }
      if (url.startsWith('/api/admin/app-platform/credentials?'))
        return { credentials: [credential], next_after: '' }
      if (url === '/api/admin/app-platform/apps/hello/secret-bindings')
        return {
          bindings: [
            { ...binding, name: 'qwen-billing', operation: 'acs3_hmac_sha256', principals: ['service'], acs3_actions: [{ action: 'QueryAccountBalance', version: '2017-12-14' }] },
          ],
        }
      throw new Error(`unexpected ${url}`)
    })
    renderPage()
    await screen.findAllByText('Upstream API')
    expect(screen.getByText('QueryAccountBalance · 2017-12-14')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appCredentials.changeUse' }))
    expect((screen.getByLabelText('appCredentials.acs3Action') as HTMLInputElement).value).toBe('QueryAccountBalance')
    expect((screen.getByLabelText('appCredentials.acs3Version') as HTMLInputElement).value).toBe('2017-12-14')
  })

  it('creates a PostgreSQL login credential from its two parts without displaying them', async () => {
    const user = userEvent.setup()
    apiMock.post.mockResolvedValue({ ...credential, id: '00000000-0000-4000-8000-000000000103' })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.createCredential/ }))
    await user.type(screen.getByLabelText('appCredentials.label'), 'Orders probe')
    await user.click(screen.getByLabelText('appCredentials.kind'))
    fireEvent.click(await screen.findByText('appCredentials.kind.postgres', { selector: '.ant-select-item-option-content' }))
    await waitFor(() => expect(screen.queryByLabelText('appCredentials.value')).toBeNull())
    await user.type(await screen.findByLabelText('appCredentials.pgUsername'), 'probe_ro')
    const password = screen.getByLabelText('appCredentials.pgPassword') as HTMLInputElement
    expect(password.type).toBe('password')
    expect(password.getAttribute('autocomplete')).toBe('off')
    await user.type(password, 'p"w\\d 1')
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/credentials', {
        label: 'Orders probe',
        value_base64: btoa(JSON.stringify({ username: 'probe_ro', password: 'p"w\\d 1' })),
      }),
    )
    expect(document.body.textContent).not.toContain('p"w')
  })

  it('reviews a PostgreSQL liveness binding for background work with its one target and never sends actions', async () => {
    const user = userEvent.setup()
    const planID = '00000000-0000-4000-8000-000000000134'
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      if (url === '/api/apps') return { apps: installed }
      if (url.startsWith('/api/admin/app-platform/credentials?'))
        return { credentials: [credential], next_after: '' }
      if (url === '/api/admin/app-platform/apps/hello/secret-bindings')
        return { bindings: [], postgres_targets: [{ id: 'orders', origin: 'postgres://db.example.com:5432/orders' }] }
      throw new Error(`unexpected ${url}`)
    })
    apiMock.post
      .mockResolvedValueOnce({
        installation_id: '00000000-0000-4000-8000-000000000105',
        plan_id: planID,
        plan_sha256: 'a'.repeat(64),
        review: {
          name: 'orders-db',
          credential_id: credential.id,
          credential_label: credential.label,
          operation: 'pg_password',
          network_policy_sha256: 'b'.repeat(64),
          network_target: 'postgres://db.example.com:5432/orders',
          network_target_sha256: 'c'.repeat(64),
          principals: ['service'],
        },
      })
      .mockResolvedValueOnce({ ...binding, name: 'orders-db', operation: 'pg_password' })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    await user.type(screen.getByLabelText('appCredentials.name'), 'orders-db')
    await user.click(screen.getByLabelText('appCredentials.credential'))
    fireEvent.click(await screen.findByText('Upstream API', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByLabelText('appCredentials.operation'))
    fireEvent.click(await screen.findByText('appCredentials.operation.pg_password', { selector: '.ant-select-item-option-content' }))
    // Background use is the only choice for this operation.
    await waitFor(() => expect(screen.queryByLabelText('appCredentials.principals')).toBeNull())
    await user.click(screen.getByRole('button', { name: 'appCredentials.reviewBinding' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/apps/hello/secret-bindings/plan', {
        name: 'orders-db',
        credential_id: credential.id,
        operation: 'pg_password',
        principals: ['service'],
        target: 'orders',
      }),
    )
    expect(Object.keys(apiMock.post.mock.calls[0][1])).not.toContain('acs3_actions')
    expect(await screen.findByText('appCredentials.bindingTarget:{"origin":"postgres://db.example.com:5432/orders"}')).toBeTruthy()
    expect(screen.getByText('appCredentials.pgDisclosure')).toBeTruthy()
    expect(screen.getByText('appCredentials.backgroundDisclosure')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appCredentials.approve' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenLastCalledWith(
        `/api/admin/app-platform/secret-binding-reviews/${planID}`,
        { plan_id: planID, plan_sha256: 'a'.repeat(64) },
      ),
    )
  })

  it('asks which PostgreSQL target when there are several and sends the chosen one', async () => {
    const user = userEvent.setup()
    apiMock.get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/apps/layout') return { apps: layout() }
      if (url === '/api/apps') return { apps: installed }
      if (url.startsWith('/api/admin/app-platform/credentials?'))
        return { credentials: [credential], next_after: '' }
      if (url === '/api/admin/app-platform/apps/hello/secret-bindings')
        return {
          bindings: [],
          postgres_targets: [
            { id: 'orders', origin: 'postgres://db.example.com:5432/orders' },
            { id: 'ledger', origin: 'postgres://db.example.com:5432/ledger' },
          ],
        }
      throw new Error(`unexpected ${url}`)
    })
    apiMock.post.mockResolvedValueOnce({
      installation_id: '00000000-0000-4000-8000-000000000105',
      plan_id: '00000000-0000-4000-8000-000000000135',
      plan_sha256: 'a'.repeat(64),
      review: { name: 'ledger-db', credential_id: credential.id, credential_label: credential.label, operation: 'pg_password', network_policy_sha256: 'b'.repeat(64), principals: ['service'] },
    })
    renderPage()
    await screen.findAllByText('Upstream API')
    await user.click(screen.getByRole('button', { name: /appCredentials.addBinding/ }))
    await user.type(screen.getByLabelText('appCredentials.name'), 'ledger-db')
    await user.click(screen.getByLabelText('appCredentials.credential'))
    fireEvent.click(await screen.findByText('Upstream API', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByLabelText('appCredentials.operation'))
    fireEvent.click(await screen.findByText('appCredentials.operation.pg_password', { selector: '.ant-select-item-option-content' }))
    await user.click(await screen.findByLabelText('appCredentials.pgTarget'))
    fireEvent.click(await screen.findByText('ledger · postgres://db.example.com:5432/ledger', { selector: '.ant-select-item-option-content' }))
    await user.click(screen.getByRole('button', { name: 'appCredentials.reviewBinding' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/app-platform/apps/hello/secret-bindings/plan', {
        name: 'ledger-db',
        credential_id: credential.id,
        operation: 'pg_password',
        principals: ['service'],
        target: 'ledger',
      }),
    )
  })

  it('opens the shared revoke confirmation from the keyboard and restores focus on Escape', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findAllByText('Upstream API')
    const revoke = screen.getByRole('button', { name: 'appCredentials.revoke' })
    revoke.focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByText('appCredentials.revokeBindingConfirm')).toBeTruthy()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(revoke))
  })

  it.each([
    ['light', theme.defaultAlgorithm],
    ['dark', theme.darkAlgorithm],
  ])('has no axe violations in %s mode', async (_mode, algorithm) => {
    renderPage(algorithm)
    await screen.findAllByText('Upstream API')
    const result = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
        region: { enabled: false },
      },
    })
    expect(result.violations).toEqual([])
  })
})
