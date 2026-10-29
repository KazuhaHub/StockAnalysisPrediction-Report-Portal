import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import type { ExportPolicy, ExportSource } from '../../../api/types'
import ExportDialog from './ExportDialog'
import type { ExportLabels } from './labels'

const apiMock = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('../../../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const source: ExportSource = { key: 'reports', formats: ['md_zip'], time_field: 'date', total: 500, columns: [{ key: 'id', default: true }], fields: [] }
const labels = { format: (k: string) => k } as unknown as ExportLabels
const policy = (over: Partial<ExportPolicy> = {}): ExportPolicy => ({ allowed: true, max_reports: 200, daily_quota: 1000, used_today: 120, remaining: 880, unlimited: false, ...over })

function renderDialog(total: number, p: ExportPolicy | null) {
  return render(
    <App>
      <ExportDialog open source={source} filter={{ op: 'and', items: [] }} columns={['id']} total={total} labels={labels} defaultName="Reports" mode="reader" policy={p} onCancel={() => {}} onCreated={() => {}} />
    </App>,
  )
}
const okButton = () => screen.getByRole('button', { name: 'export.dialog.submit' }) as HTMLButtonElement

beforeEach(() => {
  apiMock.post.mockReset()
  apiMock.post.mockResolvedValue({ id: 'j1' })
})

describe('ExportDialog in reader mode', () => {
  it('disables submit and says why when the count is over the per-export limit', async () => {
    renderDialog(201, policy())
    expect(okButton().disabled).toBe(true)
    expect(await screen.findByText('export.reader.blockTooLarge:{"n":201,"max":200}')).toBeTruthy()
  })

  it("disables submit and says why when today's allowance is short", async () => {
    renderDialog(150, policy({ remaining: 149 }))
    expect(okButton().disabled).toBe(true)
    expect(await screen.findByText('export.reader.blockQuota:{"n":150,"remaining":149}')).toBeTruthy()
  })

  it('submits to the reader endpoint, with no column choice and no format picker', async () => {
    const user = userEvent.setup()
    renderDialog(200, policy())
    expect(okButton().disabled).toBe(false)
    expect(screen.queryByText('export.dialog.format')).toBeNull()
    await user.click(okButton())
    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    const [url, body] = apiMock.post.mock.calls[0]
    expect(url).toBe('/api/exports')
    expect(body.source).toBe('reports')
    expect(body.format).toBe('md_zip')
    expect(body.columns).toBeUndefined()
  })
})
