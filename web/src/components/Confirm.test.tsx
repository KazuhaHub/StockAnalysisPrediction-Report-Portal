import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App, Button } from 'antd'

import Confirm, { confirmAction } from './Confirm'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

describe('Confirm', () => {
  // The button that does the thing names it ("Delete token"), never "OK"; the default focus is
  // Cancel, so a stray Enter does nothing; an irreversible action says so. [Apple alerts; Geist modal]
  it('names the action on its button, focuses Cancel and states irreversibility', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(
      <App>
        <Confirm title="Delete this token?" okText="Delete token" irreversible onConfirm={onConfirm}>
          <Button>open</Button>
        </Confirm>
      </App>,
    )
    await user.click(screen.getByText('open'))
    const ok = await screen.findByRole('button', { name: 'Delete token' })
    expect(ok.className).toContain('ant-btn-dangerous')
    expect(screen.getByText('confirm.irreversible')).toBeTruthy()
    await waitFor(() => expect(document.activeElement?.textContent).toBe('common.cancel'))
    await user.click(ok)
    expect(onConfirm).toHaveBeenCalled()
  })

  it('is not red for an action that is not destructive', async () => {
    const user = userEvent.setup()
    render(
      <App>
        <Confirm title="Restore this version?" okText="Restore version" danger={false} onConfirm={() => {}}>
          <Button>open</Button>
        </Confirm>
      </App>,
    )
    await user.click(screen.getByText('open'))
    const ok = await screen.findByRole('button', { name: 'Restore version' })
    expect(ok.className).not.toContain('ant-btn-dangerous')
  })
})

describe('confirmAction', () => {
  it('passes the same rules to a modal confirm', () => {
    const confirm = vi.fn()
    confirmAction({ confirm } as never, { title: 'Delete the job?', okText: 'Delete job', irreversible: true, onOk: () => {} }, (k) => k)
    const opts = confirm.mock.calls[0][0]
    expect(opts.okText).toBe('Delete job')
    expect(opts.okButtonProps).toEqual({ danger: true })
    expect(opts.autoFocusButton).toBe('cancel')
    expect(opts.cancelText).toBe('common.cancel')
  })
})
