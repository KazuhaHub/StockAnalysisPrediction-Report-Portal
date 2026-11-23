import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ReleaseDigest from './ReleaseDigest'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

afterEach(cleanup)

const digest = 'd8511fa3c3644d80d6384ec9d1b7906fa8faedeacb4684c0e9e7e4252fa2ecef'

describe('ReleaseDigest', () => {
  it('shows the first 12 hex characters, never the whole 64', () => {
    const { container } = render(<ReleaseDigest value={digest} />)
    expect(container.textContent).toContain(digest.slice(0, 12))
    expect(container.textContent).not.toContain(digest)
    expect(container.textContent).not.toContain(digest.slice(0, 13))
  })

  it('has the full value in a tooltip that keyboard focus opens too', async () => {
    const user = userEvent.setup()
    render(<ReleaseDigest value={digest} />)
    await user.tab()
    expect(document.activeElement?.textContent).toBe(digest.slice(0, 12))
    expect((await screen.findByRole('tooltip')).textContent).toBe(digest)
  })

  it('has a copy button named for what it copies, and it copies the full 64-hex value', async () => {
    const user = userEvent.setup()
    const copied: string[] = []
    render(<ReleaseDigest value={digest} />)
    vi.spyOn(navigator.clipboard, 'writeText').mockImplementation(async text => { copied.push(text) })
    await user.click(screen.getByRole('button', { name: 'appPlatform.copyReleaseDigest' }))
    await waitFor(() => expect(copied).toEqual([digest]))
  })

  it('leaves a value that is already short as it is', () => {
    const { container } = render(<ReleaseDigest value="abc" />)
    expect(container.textContent).toBe('abc')
  })
})
