import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render } from '@testing-library/react'

import TaskRow from './TaskRow'

describe('TaskRow', () => {
  // The time gives way to the actions on hover. A finished run has no actions, so without the arrow the
  // row would show an empty end cell exactly when it is being pointed at.
  it('ends an openable row with an arrow, even when it has no actions', () => {
    const { container } = render(<TaskRow tone="ok" name="Run" detail="Done" time="12:04" onOpen={() => {}} />)
    const go = container.querySelector('.rp-task-row__acts .rp-task-row__go')
    expect(go).not.toBeNull()
    expect(go?.getAttribute('aria-hidden')).toBe('true')
  })

  it('puts the arrow after the actions', () => {
    const { container } = render(
      <TaskRow tone="running" name="Run" detail="1/2" time="12:04" onOpen={() => {}} actions={<button type="button">Cancel</button>} />,
    )
    const acts = container.querySelector('.rp-task-row__acts')!
    expect(acts.lastElementChild?.classList.contains('rp-task-row__go')).toBe(true)
    expect(acts.querySelector('button')?.textContent).toBe('Cancel')
  })

  it('opens the task from the arrow, but not from an action', () => {
    const onOpen = vi.fn()
    const { container, getByText } = render(
      <TaskRow tone="running" name="Run" detail="1/2" onOpen={onOpen} actions={<button type="button">Cancel</button>} />,
    )
    fireEvent.click(getByText('Cancel'))
    expect(onOpen).not.toHaveBeenCalled()
    fireEvent.click(container.querySelector('.rp-task-row__go')!)
    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('has no arrow when the row opens nothing', () => {
    const { container } = render(<TaskRow tone="ok" name="Run" detail="Done" time="12:04" />)
    expect(container.querySelector('.rp-task-row__go')).toBeNull()
  })
})
