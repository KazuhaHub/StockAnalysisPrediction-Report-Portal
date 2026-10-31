import { act, render } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import CardTable from './CardTable'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

afterEach(() => vi.useRealTimers())

const columns = [{ title: 'Name', dataIndex: 'name', key: 'name' }]

// A first load has no rows to dim under a spinner: it shows skeleton rows instead, after a short
// delay so a fast answer never flashes. A refresh keeps the rows and antd's (delayed) spinner.
it('shows skeleton rows on a first load, not an empty table under a spinner', () => {
  vi.useFakeTimers()
  const { container } = render(<CardTable rowKey="name" columns={columns} dataSource={[]} loading />)
  expect(container.querySelector('.ant-empty')).toBeNull()
  act(() => vi.advanceTimersByTime(300))
  expect(container.querySelector('.ant-skeleton')).not.toBeNull()
  expect(container.querySelector('.ant-spin-spinning')).toBeNull()
})

it('keeps the rows on a refresh', () => {
  const { container, getByText } = render(
    <CardTable rowKey="name" columns={columns} dataSource={[{ name: 'kept' }]} loading />,
  )
  expect(getByText('kept')).toBeTruthy()
  expect(container.querySelector('.ant-skeleton')).toBeNull()
})

it('keeps an answered empty state on a reload instead of blanking it for a skeleton', () => {
  const { container, rerender } = render(<CardTable rowKey="name" columns={columns} dataSource={[]} />)
  expect(container.querySelector('.ant-empty')).not.toBeNull()
  rerender(<CardTable rowKey="name" columns={columns} dataSource={[]} loading />)
  expect(container.querySelector('.ant-empty')).not.toBeNull()
  expect(container.querySelector('.ant-skeleton')).toBeNull()
})
