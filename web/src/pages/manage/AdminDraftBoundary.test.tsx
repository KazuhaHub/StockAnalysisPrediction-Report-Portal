import { useState } from 'react'
import { App, Button, Input } from 'antd'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createMemoryRouter, Link, Route, RouterProvider, Routes } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import AdminDraftBoundary, { usePendingSettings } from './AdminDraftBoundary'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

function Editor() {
  const [value, setValue] = useState('')
  const [saving, setSaving] = useState(false)
  usePendingSettings(!!value, saving)
  return <><Input aria-label="draft" value={value} onChange={(event) => setValue(event.target.value)} />
    <Button onClick={() => setValue('')}>Save draft</Button>
    <Button onClick={() => setSaving(true)}>Start save</Button><Link to="/other">Other page</Link></>
}

function mount(initialEntries = ['/editor']) {
  const router = createMemoryRouter([{ path: '*', element: <App><Routes>
    <Route path="/editor" element={<AdminDraftBoundary><Editor /></AdminDraftBoundary>} />
    <Route path="/other" element={<div>Other page content</div>} />
  </Routes></App> }], { initialEntries })
  render(<RouterProvider router={router} />)
  return router
}

describe('pending administration drafts', () => {
  it('guards changing the organizational scope on the same settings page', async () => {
    const router = mount()
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Pending scope' } })
    await act(async () => { await router.navigate('/editor?scope=ou&ou=7') })
    await screen.findByRole('button', { name: 'manage.stay' })
    expect(router.state.location.search).toBe('')
    expect((screen.getByRole('textbox') as HTMLInputElement).value).toBe('Pending scope')
  })
  it('keeps the editor and its values when navigation is canceled', async () => {
    mount()
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Keep this' } })
    fireEvent.click(screen.getByRole('link'))
    fireEvent.click(await screen.findByRole('button', { name: 'manage.stay' }))
    expect((screen.getByRole('textbox') as HTMLInputElement).value).toBe('Keep this')
    expect(screen.queryByText('Other page content')).toBeNull()
  })

  it('handles browser back as well as links, and leaves only after an explicit choice', async () => {
    const router = mount(['/other', '/editor'])
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Pending' } })
    await act(async () => { await router.navigate(-1) })
    fireEvent.click(await screen.findByRole('button', { name: 'manage.leave' }))
    expect(await screen.findByText('Other page content')).toBeTruthy()
  })

  it('does not ask about a draft after it has been saved or discarded', async () => {
    mount()
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Saved' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save draft' }))
    fireEvent.click(screen.getByRole('link'))
    expect(await screen.findByText('Other page content')).toBeTruthy()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('prevents refresh while a draft is pending and releases the handler when clean', () => {
    mount()
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Pending' } })
    const pending = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(pending)
    expect(pending.defaultPrevented).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Save draft' }))
    const clean = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(clean)
    expect(clean.defaultPrevented).toBe(false)
  })

  it('does not let navigation detach an in-flight save', async () => {
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'Start save' }))
    fireEvent.click(screen.getByRole('link'))
    await waitFor(() => expect((screen.getByRole('button', { name: 'manage.leave' }) as HTMLButtonElement).disabled).toBe(true))
  })
})
