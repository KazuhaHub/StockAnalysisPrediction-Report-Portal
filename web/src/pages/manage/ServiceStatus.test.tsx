import { render, screen } from '@testing-library/react'
import { expect, test, vi } from 'vitest'
import { ServiceStatus } from './appsAdminParts'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string, o?: Record<string, unknown>) => `${key}${o ? JSON.stringify(o) : ''}` }) }))
vi.mock('./appsAdminModel', async (orig) => ({ ...(await orig<object>()), serviceStatus: () => ({ kind: 'some', open: 2, total: 4 }) }))

test('compact status uses the short label', () => {
  render(<ServiceStatus app={{ access: [] } as never} compact />)
  expect(screen.getByText(/apps\.status\.someShort/)).toBeTruthy()
  expect(screen.queryByText(/apps\.status\.some\{/)).toBeNull()
})
test('default status keeps the full label', () => {
  render(<ServiceStatus app={{ access: [] } as never} />)
  expect(screen.getByText(/apps\.status\.some\{/)).toBeTruthy()
})
