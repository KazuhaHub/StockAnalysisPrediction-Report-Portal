import { useEffect, useState } from 'react'
import {
  Alert,
  App,
  Button,
  Card,
  Descriptions,
  Empty,
  Input,
  Radio,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api, errText } from '../../api/client'
import type { ForecastDrySample, ForecastSettings } from '../../api/types'

// The evaluator's operating card (P9): what it is doing, what it would write, and the switch.
//
// The switch is three-way here rather than in the settings modal, because the two things an operator
// needs beside it — the readiness list and the dry-run sample — are the reasons a mode is refused or
// trusted. "On" is disabled while any readiness item fails, and the failing items are named.
//
// Staleness is computed from the heartbeat's age, not from a server flag: a page left open must go
// stale on its own, and a heartbeat that stopped is exactly the failure an operator has to see.
const STALLED_MS = 10 * 60_000

type Status = 'off' | 'dry' | 'healthy' | 'stale' | 'lagging' | 'error'

function statusOf(s: ForecastSettings, nowMs: number): Status {
  if (s.mode === '0') return 'off'
  const hb = s.heartbeat
  if (!hb) return 'stale'
  if (nowMs - Date.parse(hb.last_tick_at) > STALLED_MS) return 'stale'
  if (hb.pass.errors > 0 || hb.pass.panics > 0) return 'error'
  if (
    (hb.awaiting_gate?.length ?? 0) > 0 ||
    hb.pass.stale_valve_tripped ||
    hb.pass.oldest_due_unsettled_minutes > 360
  )
    return 'lagging'
  return s.mode === 'dry' ? 'dry' : 'healthy'
}

const TONE: Record<Status, string> = {
  off: 'default',
  dry: 'blue',
  healthy: 'green',
  stale: 'orange',
  lagging: 'orange',
  error: 'red',
}

export default function ForecastEvaluatorStatus({
  settings,
  onChanged,
}: {
  settings: ForecastSettings
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
  const [busy, setBusy] = useState(false)
  const [code, setCode] = useState(settings.gate_codes[0] ?? settings.bench)
  const [note, setNote] = useState('')
  // Re-render the age on its own clock: a page left open must go stale without a reload, and the
  // heartbeat is the only evidence that the loop is alive.
  const [nowMs, setNowMs] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [])

  const status = statusOf(settings, nowMs)
  const hb = settings.heartbeat
  const failing = settings.readiness.filter((r) => !r.ok)

  const post = async (body: Record<string, unknown>, success?: string) => {
    setBusy(true)
    try {
      await api.post('/api/admin/forecast', body)
      if (success) message.success(success)
      onChanged()
    } catch (e) {
      message.error(errText(e, t))
      throw e
    } finally {
      setBusy(false)
    }
  }

  const changeMode = (next: '0' | 'dry' | '1') => {
    if (next === settings.mode) return
    if (next !== '1') {
      void post({ mode: next }, t('common.saved')).catch(() => undefined)
      return
    }
    modal.confirm({
      title: t('forecast.eval.confirmOn'),
      content: (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <Typography.Text>{t('forecast.eval.confirmOnBody')}</Typography.Text>
          {settings.readiness.map((r) => (
            <Typography.Text key={r.key}>
              {r.ok ? '✓' : '✗'} {t(`forecast.eval.readiness.${r.key}`)}
            </Typography.Text>
          ))}
        </div>
      ),
      okText: t('forecast.eval.mode.on'),
      cancelText: t('common.cancel'),
      onOk: () => post({ mode: '1' }, t('common.saved')),
    })
  }

  const evaluateNow = async () => {
    setBusy(true)
    try {
      // No success toast: the card reloads and shows what the pass actually did, including an
      // error. "Saved" after a pass that failed would be the one message that hides it.
      await api.post('/api/admin/forecast/evaluate', {})
      onChanged()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  const recordGate = async () => {
    if (!note.trim()) return
    try {
      await post({ dry_gate: { code, note } }, t('common.saved'))
      setNote('')
    } catch {
      // the message is already shown
    }
  }

  const sampleColumns = [
    { title: t('forecast.eval.sample.set'), dataIndex: 'set_id', width: 70 },
    { title: t('forecast.eval.sample.kind'), dataIndex: 'kind' },
    { title: t('forecast.eval.sample.mark'), dataIndex: 'mark' },
    { title: t('forecast.eval.sample.code'), render: (_: unknown, r: ForecastDrySample) => `${r.market}${r.code}` },
    { title: t('forecast.eval.sample.source'), dataIndex: 'source' },
  ]

  return (
    <Card
      title={
        <Space wrap>
          <span>{t('forecast.eval.status.title')}</span>
          <Tag color={TONE[status]}>{t(`forecast.eval.status.${status}`)}</Tag>
          {hb && <Typography.Text type="secondary">{t('forecast.eval.lastTick', { at: dayjs(hb.last_tick_at).format('YYYY-MM-DD HH:mm') })}</Typography.Text>}
          {!hb && <Typography.Text type="secondary">{t('forecast.eval.never')}</Typography.Text>}
        </Space>
      }
      extra={
        <Button onClick={evaluateNow} disabled={busy || settings.mode === '0'} loading={busy}>
          {t('forecast.eval.run_now')}
        </Button>
      }
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Radio.Group
          value={settings.mode}
          onChange={(e) => changeMode(e.target.value as '0' | 'dry' | '1')}
          optionType="button"
          options={[
            { value: '0', label: t('forecast.eval.mode.off') },
            { value: 'dry', label: t('forecast.eval.mode.dry') },
            {
              value: '1',
              label: t('forecast.eval.mode.on'),
              disabled: failing.length > 0,
            },
          ]}
        />

        {settings.mode === 'dry' && <Alert type="info" showIcon title={t('forecast.eval.dryHint')} />}
        {failing.length > 0 && settings.mode !== '1' && (
          <Alert
            type="warning"
            showIcon
            title={t('forecast.eval.readiness.failed', {
              items: failing.map((r) => t(`forecast.eval.readiness.${r.key}`)).join(', '),
            })}
          />
        )}
        {(hb?.awaiting_gate?.length ?? 0) > 0 && (
          <Alert
            type="warning"
            showIcon
            title={t('forecast.eval.awaiting_gate', { codes: (hb?.awaiting_gate ?? []).join(', ') })}
          />
        )}
        {hb?.pass.last_error && <Alert type="error" showIcon title={hb.pass.last_error} />}

        <Descriptions column={{ xs: 1, sm: 2 }} size="small" bordered>
          <Descriptions.Item label={t('forecast.eval.readiness.title')}>
            <Space orientation="vertical" size={2}>
              {settings.readiness.map((r) => (
                <Typography.Text key={r.key} type={r.ok ? undefined : 'danger'}>
                  {r.ok ? '✓' : '✗'} {t(`forecast.eval.readiness.${r.key}`)}
                  {!r.ok && r.detail ? ` — ${r.detail}` : ''}
                </Typography.Text>
              ))}
            </Space>
          </Descriptions.Item>
          <Descriptions.Item label={t('forecast.eval.fetches', { n: hb ? Object.values(hb.pass.fetches).reduce((a, b) => a + b, 0) : 0 })}>
            {hb ? `${hb.pass.resolved} / ${hb.pass.due_unsettled}` : '—'}
          </Descriptions.Item>
        </Descriptions>

        <div>
          <Typography.Title level={5} style={{ marginTop: 0 }}>
            {t('forecast.eval.sample.title')}
          </Typography.Title>
          {hb && (hb.dry_sample?.length ?? 0) > 0 ? (
            <Table
              size="small"
              rowKey={(r) => `${r.set_id}-${r.kind}-${r.mark ?? ''}-${r.code}`}
              pagination={false}
              dataSource={hb.dry_sample}
              columns={sampleColumns}
            />
          ) : (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('forecast.eval.sample.empty')} />
          )}
        </div>

        <div>
          <Typography.Title level={5} style={{ marginTop: 0 }}>
            {t('forecast.eval.gate.title')}
          </Typography.Title>
          <Typography.Text type="secondary">{t('forecast.eval.gate.hint')}</Typography.Text>
          <Space wrap style={{ marginTop: 8 }}>
            <Select
              value={code}
              onChange={setCode}
              style={{ width: 160 }}
              aria-label={t('forecast.eval.gate.code')}
              options={settings.gate_codes.map((c) => ({ value: c, label: t(`forecast.bench.${c}`) }))}
            />
            <Input
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={t('forecast.eval.gate.notePlaceholder')}
              aria-label={t('forecast.eval.gate.note')}
              style={{ width: 360 }}
            />
            <Button onClick={recordGate} disabled={busy || !note.trim()}>
              {t('forecast.eval.gate.save')}
            </Button>
          </Space>
          {settings.dry_gate[code] && (
            <Typography.Text type="secondary" style={{ display: 'block', marginTop: 6 }}>
              {t('forecast.eval.gate.recorded', {
                by: settings.dry_gate[code].by,
                at: dayjs(settings.dry_gate[code].at).format('YYYY-MM-DD HH:mm'),
              })}
              {settings.dry_gate[code].note ? ` — ${settings.dry_gate[code].note}` : ''}
            </Typography.Text>
          )}
        </div>
      </Space>
    </Card>
  )
}
