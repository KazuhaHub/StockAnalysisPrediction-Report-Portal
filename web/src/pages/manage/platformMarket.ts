import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errText } from '../../api/client'
import { useAuth } from '../../auth'
import { parsePlatformMarket, parsePlatformMarketApps, type PlatformMarketApp, type PlatformMarketHint, type PlatformRelease } from '../../api/appPlatform'
import { platformRoot } from './platformLifecycle'

// The catalogues the marketplace can browse and the download-and-verify step that turns a catalogue entry into a
// verified release. Nothing here installs: a verified release is handed to the lifecycle flow, which reviews it.

export type OfficialSource = { name: string; url: string; enabled: boolean }
export function parseSources(value: unknown): OfficialSource | null {
  const official = value && typeof value === 'object' ? (value as { official?: unknown }).official : undefined
  if (!official || typeof official !== 'object') return null
  const { name, url, enabled } = official as Record<string, unknown>
  return typeof name === 'string' && typeof url === 'string' && url.startsWith('https://') && typeof enabled === 'boolean' ? { name, url, enabled } : null
}

export interface CatalogueState {
  status: 'loading' | 'ready' | 'error'
  apps: PlatformMarketApp[]
  hints: PlatformMarketHint[]
  error: string
}
export interface MarketSource { url: string; name: string; official: boolean }
/** What is known of the sources: still reading, read, the platform is not set up yet, or the read failed. */
export type SourcesState = 'loading' | 'ready' | 'uninitialised' | 'failed'
export type Staging = { appId: string; version: string } | null
/** The result of a download-and-verify: the verified release, or the reason it failed. null means it was not attempted. */
export type StageResult = { release: PlatformRelease } | { error: string } | null

/** `visible` is false while the page that shows the sources is hidden; the sources are read again when it shows. */
export function usePlatformMarket(visible: boolean = true) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const owner = useRef(user)
  owner.current = user
  const mounted = useRef(false)
  const generation = useRef<object>({})
  const staging = useRef(false)
  const [official, setOfficial] = useState<OfficialSource | null>(null)
  const [sourcesState, setSourcesState] = useState<SourcesState>('loading')
  const [custom, setCustom] = useState<string[]>([])
  const [selected, setSelected] = useState('')
  const [catalogues, setCatalogues] = useState<Record<string, CatalogueState>>({})
  const [stagingNow, setStagingNow] = useState<Staging>(null)
  const [uploading, setUploading] = useState(false)
  const [stageError, setStageError] = useState('')
  const live = (account: string | null, epoch: object) => mounted.current && owner.current === account && generation.current === epoch

  useEffect(() => {
    mounted.current = true
    const epoch = {}
    generation.current = epoch
    staging.current = false
    setOfficial(null); setSourcesState('loading')
    setCustom([]); setSelected(''); setCatalogues({}); setStagingNow(null); setUploading(false); setStageError('')
    return () => { mounted.current = false; generation.current = {} }
  }, [user])

  useEffect(() => {
    if (!visible) return
    const abort = new AbortController()
    // The built-in catalogue is a preset only. A platform that is not set up yet answers 503; any other failure
    // leaves custom addresses as the way in. The read repeats whenever the page shows again, because the switch
    // that turns the official catalogue on or off lives on another tab.
    api.get<unknown>(`${platformRoot}/market/sources`, { signal: abort.signal })
      .then(value => { if (!abort.signal.aborted) { setOfficial(parseSources(value)); setSourcesState('ready') } })
      .catch(async e => {
        if (abort.signal.aborted) return
        setOfficial(null)
        // A 503 is "not set up" only when the publishers directory says the platform is not initialised; any
        // other failure is just a failed read.
        let state: SourcesState = 'failed'
        if (e instanceof ApiError && e.status === 503) {
          try {
            const directory = await api.get<{ initialized?: boolean }>(`${platformRoot}/publishers`, { signal: abort.signal })
            if (directory.initialized === false) state = 'uninitialised'
          } catch { /* the generic failure stands */ }
        }
        if (!abort.signal.aborted) setSourcesState(state)
      })
    return () => abort.abort()
  }, [user, visible])

  const sources: MarketSource[] = [
    ...(official?.enabled ? [{ url: official.url, name: official.name, official: true }] : []),
    ...custom.filter(url => url !== official?.url).map(url => ({ url, name: url, official: false })),
  ]
  const active = sources.find(s => s.url === selected)?.url ?? sources[0]?.url ?? ''

  const load = useCallback(async (url: string) => {
    const account = user
    const epoch = generation.current
    setCatalogues(v => ({ ...v, [url]: { status: 'loading', apps: [], hints: [], error: '' } }))
    try {
      const response = await api.post<unknown>(`${platformRoot}/market/catalogue`, { catalogue_url: url })
      if (!live(account, epoch)) return
      const hints = parsePlatformMarket(response)
      if (!hints) throw new Error(t('appMarket.invalidResponse'))
      setCatalogues(v => ({ ...v, [url]: { status: 'ready', apps: parsePlatformMarketApps(response), hints, error: '' } }))
    } catch (e) {
      if (live(account, epoch)) setCatalogues(v => ({ ...v, [url]: { status: 'error', apps: [], hints: [], error: errText(e, t) } }))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user, t])

  // A source is read once, when it first becomes the one on show.
  useEffect(() => {
    if (active && !catalogues[active]) void load(active)
  }, [active, catalogues, load])

  const addCustom = (url: string) => {
    const address = url.trim()
    setCustom(v => v.includes(address) ? v : [...v, address])
    setSelected(address)
    // Entering an address again is a request to read it again.
    if (catalogues[address]) void load(address)
  }

  /**
   * Downloads and verifies one catalogue version, then re-reads its verified metadata by exact digest. Resolves to
   * the verified release or the failure (which `stageError` also holds). Catalogue copy is never promoted into a signed display
   * or permission review: only the server's re-read counts.
   */
  const stage = async (app: PlatformMarketApp, version: string, url: string = active): Promise<StageResult> => {
    const chosen = app.versions.find(v => v.version === version)
    if (staging.current || !chosen || !url) return null
    const account = user, epoch = generation.current
    staging.current = true; setStagingNow({ appId: app.app_id, version }); setStageError('')
    try {
      const result = await api.post<{ release_sha256: string }>(`${platformRoot}/market/stage`, { catalogue_url: url, release_sha256: chosen.descriptor_sha256 })
      if (!live(account, epoch)) return null
      if (result.release_sha256 !== chosen.descriptor_sha256) throw new Error(t('appMarket.verificationChanged'))
      // A newly staged package may be beyond the directory's first cursor page, so read it by exact digest.
      const response = await api.get<{ items: PlatformRelease[]; next_cursor: string }>(`${platformRoot}/releases?release_sha256=${chosen.descriptor_sha256}`)
      if (!live(account, epoch)) return null
      const verified = response.items?.length === 1 ? response.items[0] : undefined
      if (!verified || verified.release_sha256 !== chosen.descriptor_sha256 || verified.app_id !== app.app_id || verified.publisher_id !== app.publisher_id || verified.version !== version) throw new Error(t('appMarket.verificationChanged'))
      return { release: verified }
    } catch (e) {
      const error = errText(e, t)
      if (live(account, epoch)) setStageError(error)
      return live(account, epoch) ? { error } : null
    } finally { if (live(account, epoch)) { staging.current = false; setStagingNow(null) } }
  }

  /** Stages an offline .portalapp package and re-reads its verified metadata. */
  const stageOffline = async (file: Blob): Promise<StageResult> => {
    if (staging.current) return null
    const account = user, epoch = generation.current
    staging.current = true; setUploading(true); setStageError('')
    try {
      const result = await api.postBytes<{ release_sha256: string }>(`${platformRoot}/market/offline`, file, 'application/zip')
      if (!live(account, epoch)) return null
      if (!/^[0-9a-f]{64}$/.test(result.release_sha256)) throw new Error(t('appMarket.verificationChanged'))
      const response = await api.get<{ items: PlatformRelease[]; next_cursor: string }>(`${platformRoot}/releases?release_sha256=${result.release_sha256}`)
      if (!live(account, epoch)) return null
      const verified = response.items?.length === 1 ? response.items[0] : undefined
      if (!verified || verified.release_sha256 !== result.release_sha256) throw new Error(t('appMarket.verificationChanged'))
      return { release: verified }
    } catch (e) {
      const error = errText(e, t)
      if (live(account, epoch)) setStageError(error)
      return live(account, epoch) ? { error } : null
    } finally { if (live(account, epoch)) { staging.current = false; setUploading(false) } }
  }

  return {
    official, sourcesState, sources, active, setActive: setSelected, addCustom,
    catalogue: catalogues[active] as CatalogueState | undefined, catalogues,
    reload: () => { if (active) void load(active) },
    stage, stageOffline, staging: stagingNow, uploading, stageError, clearStageError: () => setStageError(''),
  }
}
export type PlatformMarket = ReturnType<typeof usePlatformMarket>

/** The verified release row of an installed app's current release, for a lifecycle action on that installation. */
export async function loadInstalledRelease(installation: string, t: (key: string) => string): Promise<PlatformRelease> {
  const settings = await api.get<{ current_release_sha256?: string }>(`${platformRoot}/installations/${installation}/settings`)
  const digest = settings.current_release_sha256
  if (!digest || !/^[0-9a-f]{64}$/.test(digest)) throw new Error(t('appStore.releaseMissing'))
  const page = await api.get<{ items: PlatformRelease[] }>(`${platformRoot}/releases?release_sha256=${digest}`)
  const row = page.items?.length === 1 ? page.items[0] : undefined
  if (!row || row.release_sha256 !== digest) throw new Error(t('appStore.releaseMissing'))
  return { ...row, installation_id: installation }
}
