import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import GeoSection, { describeDatabase } from './GeoSection'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, vars?: Record<string, unknown>) => (vars ? `${key}:${JSON.stringify(vars)}` : key) }),
}))

const pending = vi.hoisted(() => vi.fn())
vi.mock('./AdminDraftBoundary', () => ({ usePendingSettings: (dirty: boolean, saving?: boolean) => pending(dirty, saving) }))
const lastPending = () => pending.mock.calls[pending.mock.calls.length - 1]
const postSpy = vi.hoisted(() => vi.fn())

const state = vi.hoisted(() => ({
  source: 'maxmind',
  pick: '',
  files: [{ file: 'GeoLite2-City.mmdb', ok: true, info: { granularity: 'city', type: 'GeoLite2-City', build_epoch: 1753920000 } }],
}))

vi.mock('../../api/client', () => ({
  errText: (e: unknown) => String(e),
  api: {
    get: () =>
      Promise.resolve({
        status: { enabled: true, pick: state.pick, dir: '/data/geoip', loaded: true, files: state.files, info: {} },
        update: { auto: true, auto_hours: 12, source: state.source, edition: '', url: '', has_key: true, updating: false },
      }),
    post: (...args: unknown[]) => { postSpy(...args); return Promise.resolve({}) },
  },
}))

const show = () =>
  render(
    <App>
      <GeoSection />
    </App>,
  )

describe('GeoSection', () => {
  beforeEach(() => {
    state.source = 'maxmind'
    state.pick = ''
    state.files = [{ file: 'GeoLite2-City.mmdb', ok: true, info: { granularity: 'city', type: 'GeoLite2-City', build_epoch: 1753920000 } }]
  })

  // The three boxes in a row were unlabelled, so the middle one — the edition — read as a required
  // field nobody could name. Every control says what it is.
  it('labels the edition and the credential', async () => {
    show()
    await waitFor(() => expect(screen.getByText('audit.geoSourceLabel')).toBeTruthy())
    expect(screen.getByText('audit.geoEditionLabel')).toBeTruthy()
    expect(screen.getByText('audit.geoKeyLabelMaxmind')).toBeTruthy()
  })

  // The edition placeholder is the default, so leaving it blank is visibly the same as typing it.
  it('offers the default edition as the placeholder rather than as a value to keep', async () => {
    show()
    await waitFor(() => expect(screen.getByPlaceholderText('GeoLite2-City')).toBeTruthy())
    expect(screen.getByText('audit.geoEditionHint:{"def":"GeoLite2-City"}')).toBeTruthy()
  })

  // DB-IP publishes one free database and takes no account: an edition box and a key box are two
  // controls that can only mislead.
  it('drops the edition and the credential for a source that has neither', async () => {
    state.source = 'dbip'
    show()
    await waitFor(() => expect(screen.getByText('audit.geoSourceLabel')).toBeTruthy())
    expect(screen.queryByText('audit.geoEditionLabel')).toBeNull()
    expect(screen.queryByText('audit.geoKeyLabelMaxmind')).toBeNull()
    expect(screen.queryByText('audit.geoKeyLabelIpinfo')).toBeNull()
  })

  // A custom source needs the URL instead, and still no edition.
  it('asks a custom source for a URL', async () => {
    state.source = 'custom'
    show()
    await waitFor(() => expect(screen.getByText('audit.geoUrlLabel')).toBeTruthy())
    expect(screen.queryByText('audit.geoEditionLabel')).toBeNull()
  })

  // filepath.Base("") is ".", which older builds stored for "automatic" — the picker showed a lone
  // dot. A stored "." must read as the automatic choice it meant.
  it('reads a stored "." as the automatic choice', async () => {
    state.pick = '.'
    show()
    await waitFor(() => expect(screen.getByText('audit.geoAuto')).toBeTruthy())
  })

  // What each installed database IS belongs in the option that selects it. It used to be repeated
  // underneath as a bullet list of the same filenames.
  describe('describeDatabase', () => {
    const t = (key: string, vars?: Record<string, unknown>) => (vars ? `${key}:${JSON.stringify(vars)}` : key)

    it('names the file, what it resolves to, and when it was built', () => {
      expect(describeDatabase({ file: 'GeoLite2-City.mmdb', ok: true, info: { granularity: 'city', build_epoch: 1753920000 } }, t)).toBe(
        'GeoLite2-City.mmdb · audit.geoGranCity · audit.geoBuilt:{"at":"2025-07-31"}',
      )
    })

    // A database that will not open is still listed, marked: hiding it would leave the admin
    // choosing from a list that silently omits the file they just installed.
    it('marks an unreadable database instead of dropping it', () => {
      expect(describeDatabase({ file: 'broken.mmdb', ok: false }, t)).toBe('audit.geoUnreadable:{"file":"broken.mmdb"}')
    })

    // An unknown granularity is shown as it came rather than as a blank.
    it('falls back to whatever the database claims', () => {
      expect(describeDatabase({ file: 'x.mmdb', ok: true, info: { granularity: 'asn' } }, t)).toBe('x.mmdb · asn')
    })
  })
})

describe('GeoSection unsaved changes', () => {
  beforeEach(() => {
    pending.mockReset()
    postSpy.mockReset()
    state.source = 'maxmind'
    state.pick = ''
  })
  const loaded = async () => {
    show()
    await waitFor(() => expect(screen.getByText('audit.geoSourceLabel')).toBeTruthy())
  }

  it('is not pending after the settings load', async () => {
    await loaded()
    expect(pending.mock.calls.some(([dirty]) => dirty === true)).toBe(false)
    expect(lastPending()).toEqual([false, false])
  })

  it('is pending after a switch is flipped, and clean when flipped back', async () => {
    await loaded()
    const [show_] = screen.getAllByRole('switch')
    fireEvent.click(show_)
    expect(lastPending()).toEqual([true, false])
    fireEvent.click(show_)
    expect(lastPending()).toEqual([false, false])
  })

  it('is pending once a new credential is typed', async () => {
    await loaded()
    fireEvent.change(screen.getByPlaceholderText('audit.geoKeySaved'), { target: { value: 'new-key' } })
    expect(lastPending()).toEqual([true, false])
  })

  it('is clean again after a successful save', async () => {
    await loaded()
    fireEvent.click(screen.getAllByRole('switch')[0])
    fireEvent.change(screen.getByPlaceholderText('audit.geoKeySaved'), { target: { value: 'new-key' } })
    expect(lastPending()).toEqual([true, false])
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(postSpy).toHaveBeenCalledWith('/api/admin/geoip', expect.objectContaining({ enabled: false, token: 'new-key' })))
    await waitFor(() => expect(lastPending()).toEqual([false, false]))
  })
})
