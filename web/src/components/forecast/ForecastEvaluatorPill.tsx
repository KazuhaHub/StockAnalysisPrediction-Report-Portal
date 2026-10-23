import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Descriptions, Drawer, Typography, theme } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api } from '../../api/client'
import type { ForecastSettings } from '../../api/types'
import { evaluatorStatus, LAGGING_MINUTES, STALLED_MS, type EvaluatorStatus } from '../../lib/forecastEvaluator'
import { forecastLinkColor } from '../../lib/forecastPresentation'
import Quiet from './Quiet'

// The administrator's evaluator status on the record page: a pill in the header, a drawer behind it,
// and the full console on the operations page. Readers never see it; the record is theirs and the
// evaluator is an operator's concern.
//
// It reads the one admin route that carries the heartbeat (the reader response only knows the
// evaluator's mode), and a failed read is this component's own: the record beside it does not wait
// for it and does not hide behind it.

const MODE_KEY = { '0': 'off', dry: 'dry', '1': 'on' } as const

export default function ForecastEvaluatorPill() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const [settings, setSettings] = useState<ForecastSettings | null>(null)
  const [failed, setFailed] = useState(false)
  const [open, setOpen] = useState(false)
  // Re-render the age on its own clock: a page left open must go stale without a reload.
  const [nowMs, setNowMs] = useState(() => Date.now())

  const load = useCallback(() => {
    setFailed(false)
    api
      .get<ForecastSettings>('/api/admin/forecast')
      // A reply that is not a settings object is a failed read, not something to render.
      .then((value) => (Array.isArray(value?.readiness) ? setSettings(value) : setFailed(true)))
      .catch(() => setFailed(true))
  }, [])
  useEffect(() => {
    load()
  }, [load])
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [])

  if (failed) {
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
        <Quiet style={{ fontSize: 12 }}>{t('forecast.eval.pill.unavailable')}</Quiet>
        <Button size="small" type="link" onClick={load}>{t('common.retry')}</Button>
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
        return <Alert type="info" showIcon title={t('forecast.eval.offHint')} />
      case 'dry':
        return <Alert type="info" showIcon title={t('forecast.eval.dryHint')} />
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
        onClick={() => setOpen(true)}
        icon={<span aria-hidden="true" style={{ display: 'inline-block', width: 8, height: 8, borderRadius: '50%', background: dot[status] }} />}
      >
        {t('forecast.eval.pill', { status: statusText })}
      </Button>
      <Drawer open={open} onClose={() => setOpen(false)} title={t('forecast.eval.status.title')} size={420} destroyOnHidden>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <Descriptions column={1} size="small">
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
