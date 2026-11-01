import { expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import BatchTargetPicker from './BatchTargetPicker'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

// The visible label is also the picker's accessible name.
it('names the workflow picker by its label', () => {
  render(<BatchTargetPicker targets={[]} value={undefined} onChange={() => {}} />)
  expect(screen.getByRole('combobox', { name: 'batch.target' })).toBeTruthy()
})
