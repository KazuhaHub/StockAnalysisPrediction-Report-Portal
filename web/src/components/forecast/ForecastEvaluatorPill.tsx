import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, Button, Descriptions, Drawer, theme } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api } from '../../api/client'
import type { ForecastSettings } from '../../api/types'
import { evaluatorStatus, LAGGING_MINUTES, REFRESH_MS, REREAD_MIN_MS, STALLED_MS, type EvaluatorStatus } from '../../lib/forecastEvaluator'
import { forecastLinkColor } from '../../lib/forecastPresentation'
import Quiet from './Quiet'

// The administrator's evaluator status on the record page: a pill in the header, a drawer behind it,
// and the full console on the operations page. Readers never see it; the record is theirs and the
// evaluator is an operator's concern.
//
// It reads the one admin route that carries the heartbeat (the reader response only knows the
// evaluator's mode), and a failed read is this component's own: the record beside it does not wait
// for it and does not hide behind it.
//
// The age of the heartbeat is computed on this page's clock, so the heartbeat itself has to be read
// again: a page left open for ten minutes would otherwise call a healthy evaluator stale. The route it
// reads counts rows in the ledger and the reports table, so that read is made every few minutes (see
// REFRESH_MS), not every minute, and a tab that merely becomes visible again does not repeat a read
// that has just been made.

const MODE_KEY = { '0': 'off', dry: 'dry', '1': 'on' } as const

export default function ForecastEvaluatorPill() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const [settings, setSettings] = useState<ForecastSettings | null>(null)
  const [failed, setFailed] = useState(false)
  const [open, setOpen] = useState(false)
  // Re-render the age on its own clock: a page left open must go stale without a reload.
  const [nowMs, setNowMs] = useState(() => Date.now())

  // Only the newest request may write: a slow answer must not overwrite a newer one, and nothing
  // writes once the pill is gone.
  const latest = useRef(0)
  const askedAt = useRef(0)
  const supersede = useCallback(() => ++latest.current, [])
  const load = useCallback(() => {
    const mine = supersede()
    askedAt.current = Date.now()
    api
      .get<ForecastSettings>('/api/admin/forecast')
      .then((value) => {
        if (mine !== latest.current) return
        // A reply that is not a settings object is a failed read, not something to render.
        if (Array.isArray(value?.readiness)) {
          setSettings(value)
          setFailed(false)
        } else setFailed(true)
      })
      .catch(() => {
        if (mine === latest.current) setFailed(true)
      })
  }, [supersede])
  useEffect(() => {
    load()
    const refresh = setInterval(() => {
      if (!document.hidden) load()
    }, REFRESH_MS)
    // A tab that was in the background for a while has an old snapshot and a new clock. One that was
    // read a moment ago does not: flicking between tabs is not a reason to count the ledger again.
    const visible = () => {
      if (!document.hidden && Date.now() - askedAt.current >= REREAD_MIN_MS) load()
    }
    document.addEventListener('visibilitychange', visible)
    return () => {
      clearInterval(refresh)
      document.removeEventListener('visibilitychange', visible)
      supersede()
    }
  }, [load, supersede])
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [])

  if (failed) {
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
        <Quiet style={{ fontSize: 12 }}>{t('forecast.eval.pill.unavailable')}</Quiet>
        <Button size="small" type="link" onClick={load} style={{ color: forecastLinkColor(token) }}>{t('common.retry')}</Button>
      </span>
    )
  }
  if (!settings) return null

  const status: EvaluatorStatus = evaluatorStatus(settings, nowMs)
  const hb = settings.heartbeat
  const dot: Record<EvaluatorStatus, string> = {
    off: token.colorTextQuaternary,
    dry: token.colorPrimary,
    healthy: token.colorSuccess,
    stale: token.colorError,
    lagging: token.colorWarning,
    error: token.colorError,
  }
  const minutes = hb ? Math.max(0, Math.floor((nowMs - Date.parse(hb.last_tick_at)) / 60_000)) : 0
  const statusText = t(`forecast.eval.status.${status}`)

  const explanation = () => {
    switch (status) {
      case 'off':
        return <Alert type="info" showIcon title={t('forecast.eval.pill.off_hint')} />
      case 'dry':
        return <Alert type="info" showIcon title={t('forecast.eval.pill.dry_hint')} />
      case 'healthy':
        return hb ? (
          <Alert type="success" showIcon title={t('forecast.eval.pill.pass', { sets: hb.pass.sets, resolved: hb.pass.resolved })} />
        ) : null
      case 'stale':
        return <Alert type="error" showIcon title={t('forecast.eval.pill.stale_hint', { minutes: STALLED_MS / 60_000 })} />
      case 'lagging':
        return <Alert type="warning" showIcon title={t('forecast.eval.pill.lagging_hint', { hours: LAGGING_MINUTES / 60 })} />
      case 'error':
        return (
          <Alert
            type="error"
            showIcon
            title={hb?.pass.last_error || t('forecast.eval.pill.error_hint')}
            description={hb?.pass.last_error ? t('forecast.eval.pill.error_hint') : undefined}
          />
        )
    }
  }

  return (
    <>
      <Button
        size="small"
        shape="round"
        aria-haspopup="dialog"
        onClick={() => {
          setOpen(true)
          // What the drawer shows should be current, not whatever the last timer tick left.
          load()
        }}
        icon={<span aria-hidden="true" style={{ display: 'inline-block', width: 8, height: 8, borderRadius: '50%', background: dot[status] }} />}
      >
        {t('forecast.eval.pill', { status: statusText })}
      </Button>
      <Drawer open={open} onClose={() => setOpen(false)} title={t('forecast.eval.status.title')} size={420} destroyOnHidden>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {/* antd draws the field names in its tertiary colour, which is under AA on white (3.4:1). */}
          <Descriptions column={1} size="small" styles={{ label: { color: token.colorTextSecondary } }}>
            <Descriptions.Item label={t('forecast.eval.pill.state')}>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <span aria-hidden="true" style={{ display: 'inline-block', width: 8, height: 8, borderRadius: '50%', background: dot[status] }} />
                {statusText}
              </span>
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.eval.pill.mode')}>{t(`forecast.eval.mode.${MODE_KEY[settings.mode]}`)}</Descriptions.Item>
            <Descriptions.Item label={t('forecast.eval.pill.last_run')}>
              {hb ? dayjs(hb.last_tick_at).format('YYYY-MM-DD HH:mm') : t('forecast.eval.never')}
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.eval.pill.heartbeat')}>
              {hb ? (minutes < 1 ? t('forecast.eval.pill.just_now') : t('forecast.eval.pill.minutes_ago', { n: minutes })) : '—'}
            </Descriptions.Item>
          </Descriptions>
          {explanation()}
          <Link to="/manage/forecast" style={{ color: forecastLinkColor(token) }}>{t('forecast.eval.pill.operations')}</Link>
        </div>
      </Drawer>
    </>
  )
}
