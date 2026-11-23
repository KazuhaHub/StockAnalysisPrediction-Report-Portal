import { useMemo, useRef, useState } from 'react'
import { Alert, Button, Empty, Input, Modal, Select, Space, Spin, Typography } from 'antd'
import { CloudUploadOutlined, LinkOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import LoadGate from '../../components/LoadGate'
import { appDisplayText } from '../../lib/appDisplayV2'
import { usePrefs } from '../../prefs'
import PlatformLifecycleModal from './PlatformLifecycleModal'
import { MarketCard, MarketDetailModal } from './appStoreParts'
import { useAppsCatalog, type AdminApp } from './appsAdminModel'
import { AppsBreadcrumb } from './appsAdminParts'
import { sourceName, useAppsConsoleFlow } from './appsConsoleFlow'

/** The catalogue page: apps the catalogue offers that are not installed yet (decision D9). */
export default function AppsInstallPage() {
  const { apps, loading, error, reload } = useAppsCatalog()
  return <LoadGate loading={loading} error={error} onRetry={reload}><InstallBody apps={apps} reload={reload} /></LoadGate>
}

function InstallBody({ apps, reload }: { apps: AdminApp[]; reload: (opts?: { quiet?: boolean }) => Promise<boolean> }) {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const navigate = useNavigate()
  const flow = useAppsConsoleFlow({ apps, reload })
  const { lifecycle, market, busy } = flow
  const [search, setSearch] = useState('')
  const [customOpen, setCustomOpen] = useState(false)
  const [customUrl, setCustomUrl] = useState('')
  const file = useRef<HTMLInputElement>(null)
  const customValid = (() => { try { return new URL(customUrl.trim()).protocol === 'https:' } catch { return false } })()
  const matches = (text: string) => !search.trim() || text.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())
  const installedKeys = useMemo(() => new Set(apps.filter(a => a.platform && a.publisherId && a.appId).map(a => `${a.publisherId}/${a.appId}`)), [apps])
  const shown = flow.catalogueApps.filter(c => !installedKeys.has(`${c.publisher_id}/${c.app_id}`) && matches(`${appDisplayText(c.app_id, c.display, lang).name} ${c.app_id} ${c.publisher_id}`))
  const goTrust = () => navigate('/manage/apps?tab=publishers')

  const openCustom = () => {
    market.addCustom(customUrl)
    setCustomOpen(false); setCustomUrl('')
  }

  const body = () => {
    const state = market.catalogue
    if (market.sourcesState === 'uninitialised') return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('appStore.platformNotReady')}><Button onClick={goTrust}>{t('appStore.openTrust')}</Button></Empty>
    if (!market.sources.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={market.sourcesState === 'loading' ? <Spin /> : market.sourcesState === 'failed' ? t('appStore.sourcesFailed') : t('appStore.sourceNone')} />
    // A source read again keeps showing what it listed; only a first read waits behind the spinner.
    if (!state || (state.status === 'loading' && !state.apps.length)) return <div className="rp-app-grid-state"><Spin /></div>
    if (state.status === 'error') return <Alert type="error" title={state.error} action={<Button onClick={market.reload}>{t('appStore.retry')}</Button>} />
    if (!shown.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={search.trim() ? t('appStore.noMatches') : t('appStore.catalogueEmpty')} />
    return <div className="rp-app-grid">
      {shown.map(c => {
        const copy = appDisplayText(c.app_id, c.display, lang)
        const key = `${c.publisher_id}/${c.app_id}`
        return <MarketCard key={key} app={c} name={copy.name} description={copy.description} error={flow.cardErrors[key]}
          staging={market.staging?.appId === c.app_id ? market.staging : null} blocked={busy} onOpen={() => flow.openDetail(c)} onInstall={() => void flow.quickInstall(c)} />
      })}
    </div>
  }

  return <Space orientation="vertical" size={16} style={{ width: '100%' }}>
    <AppsBreadcrumb page={t('appStore.install')} />
    <Typography.Title level={4} style={{ margin: 0 }}>{t('appStore.install')}</Typography.Title>
    <div className="rp-app-toolbar">
      <Input.Search allowClear className="rp-app-search" aria-label={t('apps.search')} placeholder={t('apps.search')} value={search} onChange={e => setSearch(e.target.value)} />
      <Space wrap>
        {market.sources.length > 0 && <Select className="rp-market-source" aria-label={t('appStore.source')} value={market.active} onChange={market.setActive} popupMatchSelectWidth={false}
          options={market.sources.map(s => ({ value: s.url, label: `${t('appStore.source')}: ${sourceName(s.name, s.url, s.official)}` }))} />}
        <Button icon={<LinkOutlined aria-hidden="true" />} disabled={busy} onClick={() => setCustomOpen(true)}>{t('appStore.install.custom')}</Button>
        <Button icon={<CloudUploadOutlined aria-hidden="true" />} loading={market.uploading} disabled={busy && !market.uploading} onClick={() => file.current?.click()}>{market.uploading ? t('appStore.offlineBusy') : t('appStore.install.offline')}</Button>
        <input ref={file} type="file" accept=".portalapp" hidden data-testid="offline-package" aria-label={t('appStore.install.offline')} onChange={e => void flow.uploaded(e)} />
      </Space>
    </div>

    {lifecycle.hiddenPending && <Alert type="warning" showIcon title={t('appPlatform.uncertain')} action={<Button onClick={lifecycle.reopen}>{t('appPlatform.retrySame')}</Button>} />}

    <section aria-label={t('appStore.install')}>{body()}</section>

    <MarketDetailModal app={flow.detail} installed={flow.detail ? flow.installedOf(flow.detail) : undefined} lang={lang} staging={market.staging && flow.detail && market.staging.appId === flow.detail.app_id ? market.staging : null}
      blocked={busy} error={flow.detailError || market.stageError} onClose={() => flow.setDetail(null)}
      onInstall={version => flow.detail && void flow.installFromDetail(flow.detail, version)} onOpenTrust={() => { flow.setDetail(null); goTrust() }} />

    <Modal open={customOpen} title={t('appStore.customTitle')} okText={t('appStore.customOpen')} cancelText={t('appPlatform.cancel')} okButtonProps={{ disabled: !customValid }} onOk={openCustom} onCancel={() => setCustomOpen(false)} destroyOnHidden>
      <Space orientation="vertical" style={{ width: '100%' }}>
        <Typography.Text>{t('appStore.customPrompt')}</Typography.Text>
        <Input aria-label={t('appStore.customPrompt')} placeholder="https://…/index.json" maxLength={2048} value={customUrl} status={customUrl && !customValid ? 'error' : undefined} onChange={e => setCustomUrl(e.target.value)} onPressEnter={() => { if (customValid) openCustom() }} />
        {customUrl && !customValid && <Typography.Text type="secondary">{t('appStore.customInvalid')}</Typography.Text>}
      </Space>
    </Modal>

    <PlatformLifecycleModal lifecycle={lifecycle} backTo="/manage/apps" />
  </Space>
}
