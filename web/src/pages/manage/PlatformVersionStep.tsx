import { useRef, useState } from 'react'
import { Alert, Button, Progress, Radio, Skeleton, Space, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { compareVersions, latestVersion, offeredUpdate } from '../../lib/semver'
import { stagingPercent, stagingText } from './appStoreParts'
import { releaseAction, type PlatformLifecycle } from './platformLifecycle'
import { usePlatformMarket } from './platformMarket'

/**
 * The version step of the lifecycle dialog: which version a switch or an update goes to. The catalogue is read only
 * while this step shows (`usePlatformMarket(active)` stays idle otherwise), so a page that offers the action loads
 * nothing in advance. Continue downloads and verifies the chosen version, then hands the verified release to the
 * lifecycle flow, which reviews, confirms and follows it in the same dialog.
 */
export function useVersionChoice(lifecycle: PlatformLifecycle) {
  const { t } = useTranslation()
  const picker = lifecycle.picker
  // Live for the whole flow that started from the choice, so Back from the review finds the list as it was left.
  const market = usePlatformMarket(!!picker)
  const subject = picker?.subject
  const latestPicker = useRef(picker)
  latestPicker.current = picker
  const [chosen, setChosen] = useState<{ key: string; version: string } | null>(null)
  const [problem, setProblem] = useState('')
  const key = picker ? `${picker.subject.installationId}:${picker.mode}` : ''

  const entry = subject && market.catalogue
    ? market.catalogue.apps.find(c => c.publisher_id === subject.publisherId && c.app_id === subject.appId)
    : undefined
  const sorted = entry ? [...entry.versions].sort((a, b) => compareVersions(b.version, a.version)) : []
  const installed = subject?.version
  const versions = picker?.mode === 'update' && installed ? sorted.filter(v => compareVersions(v.version, installed) > 0) : sorted
  const latest = latestVersion(sorted.map(v => v.version))
  const offered = installed ? offeredUpdate(installed, sorted.map(v => v.version)) : undefined
  const fallback = offered && versions.some(v => v.version === offered) ? offered : ''
  const picked = chosen?.key === key && chosen.version !== installed && versions.some(v => v.version === chosen.version) ? chosen.version : fallback

  const state: 'loading' | 'sourcesFailed' | 'notReady' | 'noSource' | 'catalogueFailed' | 'notListed' | 'ready' =
    market.sourcesState === 'loading' ? 'loading'
    : market.sourcesState === 'failed' ? 'sourcesFailed'
    : market.sourcesState === 'uninitialised' ? 'notReady'
    : !market.sources.length ? 'noSource'
    : !market.catalogue || (market.catalogue.status === 'loading' && !entry) ? 'loading'
    : market.catalogue.status === 'error' ? 'catalogueFailed'
    : !entry ? 'notListed'
    : 'ready'

  const staging = !!market.staging
  const canContinue = state === 'ready' && !!picked && !staging

  const next = async () => {
    if (!entry || !subject || !canContinue) return
    const asked = latestPicker.current
    setProblem('')
    const result = await market.stage(entry, picked)
    // The dialog was closed, or another flow opened, while the package downloaded: nothing of it is wanted any more.
    if (!result || latestPicker.current !== asked) return
    if ('error' in result) { setProblem(result.error); return }
    const flow = releaseAction(result.release, subject.installationId, t)
    if ('error' in flow) { setProblem(flow.error); return }
    const started = lifecycle.begin(flow.release, flow.action, undefined, true)
    if (started !== 'started') setProblem(t(started === 'pending' ? 'appStore.busyPending' : 'appStore.busyWorking'))
  }

  return {
    state, market, versions, latest, installed, picked, staging, canContinue, next,
    choose: (version: string) => setChosen({ key, version }),
    problem: problem || market.stageError,
  }
}
export type VersionChoice = ReturnType<typeof useVersionChoice>

export function VersionStepBody({ choice }: { choice: VersionChoice }) {
  const { t } = useTranslation()
  const { state, market } = choice
  if (state === 'loading') return <div role="status" aria-live="polite">
    <Skeleton active title={false} paragraph={{ rows: 3 }} />
    <span className="rp-sr-only">{t('appStore.versionsLoading')}</span>
  </div>
  if (state === 'sourcesFailed') return <Alert type="error" showIcon title={t('appStore.sourcesFailed')} action={<Button size="small" onClick={market.reloadSources}>{t('common.retry')}</Button>} />
  if (state === 'catalogueFailed') return <Alert type="error" showIcon title={market.catalogue?.error || t('appStore.catalogueFailed')} action={<Button size="small" onClick={market.reload}>{t('common.retry')}</Button>} />
  if (state === 'notReady') return <Alert type="warning" showIcon title={t('appStore.platformNotReady')} />
  if (state === 'noSource') return <Alert type="info" showIcon title={t('appStore.sourceNone')} />
  if (state === 'notListed') return <Alert type="info" showIcon title={t('appStore.versionsNotListed')} />
  const { versions, latest, installed, picked } = choice
  const percent = market.staging ? stagingPercent(market.staging) : undefined
  return <Space orientation="vertical" size={12} style={{ width: '100%' }}>
    {!versions.length
      ? <Alert type="success" showIcon title={t('appStore.upToDate')} />
      : <Radio.Group className="rp-version-list" value={picked || undefined} disabled={choice.staging} onChange={e => choice.choose(e.target.value as string)} aria-label={t('appStore.versions')}>
        {versions.map(v => <Radio key={v.version} value={v.version} disabled={v.version === installed}>
          <Space size={6}>
            <Typography.Text>{v.version}</Typography.Text>
            {v.version === latest && <Tag>{t('appStore.latest')}</Tag>}
            {v.version === installed && <Tag>{t('appStore.current')}</Tag>}
          </Space>
        </Radio>)}
      </Radio.Group>}
    {market.staging && <div role="status" aria-live="polite">
      <Typography.Text type="secondary">{stagingText(market.staging, t)}</Typography.Text>
      {percent !== undefined && <Progress percent={percent} size="small" aria-label={stagingText(market.staging, t)} />}
    </div>}
    {choice.problem && <Alert type="error" showIcon title={choice.problem} />}
  </Space>
}
