import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ExportSource } from '../../../api/types'
import ColumnsDialog from './ColumnsDialog'
import { useExportLabels } from './labels'

vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const source = {
  key: 'audit',
  formats: ['csv'],
  fields: [],
  time_field: 'at',
  total: 1,
  columns: [{ key: 'id', default: true }, { key: 'at', default: true }, { key: 'ip', default: false }],
} as ExportSource

function Harness({ value, onSave }: { value: string[]; onSave: (k: string[]) => void }) {
  const labels = useExportLabels()
  return <ColumnsDialog open source={source} value={value} labels={labels} onCancel={() => {}} onSave={onSave} />
}

describe('ColumnsDialog', () => {
  it('removes a column and saves the remaining order', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    render(<Harness value={['id', 'at', 'ip']} onSave={onSave} />)
    await user.click(await screen.findByRole('button', { name: 'export.columns.remove:{"name":"export.column.at"}' }))
    await user.click(screen.getByRole('button', { name: 'export.columns.save' }))
    expect(onSave).toHaveBeenCalledWith(['id', 'ip'])
  })

  it('will not remove the last column', async () => {
    render(<Harness value={['id']} onSave={() => {}} />)
    const remove = (await screen.findByRole('button', { name: /export\.columns\.remove/ })) as HTMLButtonElement
    expect(remove.disabled).toBe(true)
  })

  it('resets to the source defaults', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    render(<Harness value={['ip']} onSave={onSave} />)
    await user.click(await screen.findByRole('button', { name: 'export.columns.reset' }))
    await user.click(screen.getByRole('button', { name: 'export.columns.save' }))
    expect(onSave).toHaveBeenCalledWith(['id', 'at'])
  })
})
