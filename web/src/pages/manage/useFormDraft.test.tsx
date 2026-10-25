import { useState } from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App, Button, ConfigProvider, Form, Input } from 'antd'
import { describe, expect, it, vi } from 'vitest'
import useFormDraft from './useFormDraft'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
function Editor() {
  const [form] = Form.useForm()
  const [open, setOpen] = useState(false)
  const draft = useFormDraft(form, open, false, () => setOpen(false))
  return <><Button onClick={() => { form.setFieldsValue({ name: 'Saved name' }); setOpen(true) }}>Open editor</Button>
    {open && <Form form={form}><Form.Item name="name"><Input aria-label="Name" /></Form.Item><Button onClick={draft.close}>Close editor</Button></Form>}</>
}
describe('Modal draft boundaries', () => {
  it('keeps edited values when closing is canceled, and closes only after discard', async () => {
    render(<ConfigProvider theme={{ token: { motion: false } }}><App><Editor /></App></ConfigProvider>)
    fireEvent.click(screen.getByRole('button', { name: 'Open editor' }))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Pending name' } })
    fireEvent.click(screen.getByRole('button', { name: 'Close editor' }))
    fireEvent.click(await screen.findByRole('button', { name: 'manage.stay' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect((screen.getByRole('textbox') as HTMLInputElement).value).toBe('Pending name')
    fireEvent.click(screen.getByRole('button', { name: 'Close editor' }))
    fireEvent.click(await screen.findByRole('button', { name: 'manage.leave' }))
    await waitFor(() => expect(screen.queryByRole('textbox')).toBeNull())
  })
})
