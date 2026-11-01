import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import BatchExecutionFields from './BatchExecutionFields'
import BatchTargetPicker from './BatchTargetPicker'
import type { BatchTarget } from '../api/types'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

describe('BatchExecutionFields', () => {
  it('lists retries, then concurrency, then the page-specific priority control', () => {
    render(
      <BatchExecutionFields maxRetries={2} onMaxRetries={() => {}} rowConcurrency={1} onRowConcurrency={() => {}}>
        <label>priority-slot</label>
      </BatchExecutionFields>,
    )
    const labels = Array.from(document.querySelectorAll('.rp-batch-setting-fields > label')).map((node) => node.textContent)
    expect(labels).toEqual(['batch.maxRetries', 'batch.rowConcurrency', 'priority-slot'])
  })

  it('bounds retries at 5 and concurrency at the ceiling it is given, defaulting to 20', () => {
    const { rerender } = render(<BatchExecutionFields maxRetries={2} onMaxRetries={() => {}} rowConcurrency={1} onRowConcurrency={() => {}} />)
    const [retries, concurrency] = screen.getAllByRole('spinbutton')
    expect(retries.getAttribute('aria-valuemax')).toBe('5')
    expect(retries.getAttribute('aria-valuemin')).toBe('0')
    expect(concurrency.getAttribute('aria-valuemax')).toBe('20')
    expect(concurrency.getAttribute('aria-valuemin')).toBe('1')
    rerender(<BatchExecutionFields maxRetries={2} onMaxRetries={() => {}} rowConcurrency={1} onRowConcurrency={() => {}} maxConcurrency={8} />)
    expect(screen.getAllByRole('spinbutton')[1].getAttribute('aria-valuemax')).toBe('8')
  })

  it('reports edits, and reads a cleared field as the lowest allowed value', () => {
    const onRetries = vi.fn()
    const onConcurrency = vi.fn()
    render(<BatchExecutionFields maxRetries={2} onMaxRetries={onRetries} rowConcurrency={3} onRowConcurrency={onConcurrency} />)
    const [retries, concurrency] = screen.getAllByRole('spinbutton')
    fireEvent.change(retries, { target: { value: '4' } })
    expect(onRetries).toHaveBeenLastCalledWith(4)
    fireEvent.change(concurrency, { target: { value: '' } })
    expect(onConcurrency).toHaveBeenLastCalledWith(1)
  })
})

describe('BatchTargetPicker', () => {
  const targets: BatchTarget[] = [{ id: 1, plugin_slug: 'dify', plugin_name: 'Dify', name: 'Research', created_at: '' }]

  it('shows the shared label and the chosen workflow', () => {
    render(<BatchTargetPicker targets={targets} value={1} onChange={() => {}} />)
    expect(screen.getByText('batch.target')).toBeTruthy()
    expect(screen.getByText('Research（Dify）')).toBeTruthy()
  })

  it('keeps a labelled slot for a deleted target and shows the warning under the row', () => {
    render(
      <BatchTargetPicker targets={targets} value={9} onChange={() => {}} missing={{ id: 9, label: 'gone-label' }} warning="pick another" />,
    )
    expect(screen.getByText('gone-label')).toBeTruthy()
    expect(screen.getByText('pick another')).toBeTruthy()
  })
})
