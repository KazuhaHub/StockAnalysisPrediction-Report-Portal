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
import type { ForecastDrySample, ForecastReadinessItem, ForecastSettings } from '../../api/types'
import ForecastBacklog from './ForecastBacklog'
import { evaluatorStatus, EVALUATOR_TAG_COLOR } from '../../lib/forecastEvaluator'

// The evaluator's operating card (P9): what it is doing, what it would write, and the switch.
//
// The switch is three-way here rather than in the settings modal, because the two things an operator
// needs beside it — the readiness list and the dry-run sample — are the reasons a mode is refused or
// trusted. Switching "On" is refused while any readiness item fails, and the failing items are named
// (once on, the option stays enabled: the dry-pass item fails by construction in that mode). The
// state itself comes from lib/forecastEvaluator, shared with the record page's pill.
//
// The card changes shape with the mode. Off and dry are the setup phase: the readiness list, and in
// dry the sample and the gate form, are what the operator works through. Once on, those are history
// (the dry-pass item can never pass again by design), so the card shows the last pass and the backlog,
// and brings back only what still needs a hand: a failing item that still matters, and the gate form
// while a benchmark is waiting for its check.

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
  // The operator's pick, remembered with the suggestion it overrode: a quiet reload keeps this card
  // mounted, and a newly scheduled or newly awaited benchmark must replace a pick made before it.
  const [pick, setPick] = useState<{ code: string; against: string } | null>(null)
  const [note, setNote] = useState('')
  // Re-render the age on its own clock: a page left open must go stale without a reload, and the
  // heartbeat is the only evidence that the loop is alive.
  const [nowMs, setNowMs] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [])

  const status = evaluatorStatus(settings, nowMs)
  const hb = settings.heartbeat
  const on = settings.mode === '1'
  const failing = settings.readiness.filter((r) => !r.ok)
  // Once on, the dry-pass item fails by construction and the primary-source item has its own alert.
  // A missing gate the evaluator is already waiting on is named once, by the awaiting-gate alert.
  const stillFailing = failing.filter(
    (r) => r.key !== 'dry_pass' && r.key !== 'ledger_chain' && !(r.key === 'dry_gate' && (hb?.awaiting_gate?.length ?? 0) > 0),
  )
  // A benchmark scheduled for the next cohort with no check would only ever be planned, never written.
  const nextUnchecked = settings.bench_next && !settings.dry_gate[settings.bench_next] ? settings.bench_next : ''
  const awaiting = hb?.awaiting_gate ?? []
  const gateCodes = [...new Set([...awaiting, ...(nextUnchecked ? [nextUnchecked] : []), ...settings.gate_codes])]
  const suggested = awaiting[0] ?? (nextUnchecked || (settings.gate_codes[0] ?? settings.bench))
  const code = pick && pick.against === suggested && gateCodes.includes(pick.code) ? pick.code : suggested
  const showGate = settings.mode === 'dry' || (on && (awaiting.length > 0 || nextUnchecked !== ''))
  const fetches = hb ? Object.values(hb.pass.fetches).reduce((a, b) => a + b, 0) : 0
  // A failing item is worded from its reason code, with the benchmarks it names in their display
  // names; the server's English detail is only the fallback for a server that sends no reason.
  const readinessDetail = (r: ForecastReadinessItem) => {
    if (!r.reason) return r.detail ?? ''
    const key = `forecast.eval.readiness.reason.${r.reason}`
    const codes = (r.codes ?? []).map((c) => t(`forecast.bench.${c}`)).join(t('forecast.listSeparator'))
    const text = t(key, { codes, count: r.count ?? 0 })
    // t answers with the key itself for a reason this page has no wording for (a newer server).
    return text === key ? (r.detail ?? '') : text
  }

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
          <Tag color={EVALUATOR_TAG_COLOR[status]}>{t(`forecast.eval.status.${status}`)}</Tag>
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
              // Already on: the dry-pass item fails by construction, and disabling the selected
              // option would grey out the very mode the evaluator is in.
              disabled: !on && failing.length > 0,
            },
          ]}
        />

        {settings.mode === '0' && <Alert type="info" showIcon title={t('forecast.eval.offHint')} />}
        {settings.mode === 'dry' && <Alert type="info" showIcon title={t('forecast.eval.dryHint')} />}
        {settings.readiness.some((item) => item.key === 'ledger_chain' && !item.ok) && (
          <Alert type="warning" showIcon title={t('quoteAdmin.ledgerNoPrimary')} />
        )}
        {failing.length > 0 && settings.mode !== '1' && (
          <Alert
            type="warning"
            showIcon
            title={t('forecast.eval.readiness.failed', {
              items: failing.map((r) => t(`forecast.eval.readiness.${r.key}`)).join(', '),
            })}
          />
        )}
        {on && stillFailing.map((r) => (
          <Alert
            key={r.key}
            type="warning"
            showIcon
            title={readinessDetail(r) || t(`forecast.eval.readiness.${r.key}`)}
          />
        ))}
        {on && nextUnchecked && (
          <Alert
            type="warning"
            showIcon
            title={t('forecast.eval.gate.nextUnchecked', { bench: t(`forecast.bench.${nextUnchecked}`), from: settings.bench_next_from ?? '' })}
          />
        )}
        {(hb?.awaiting_gate?.length ?? 0) > 0 && (
          <Alert
            type="warning"
            showIcon
            title={t('forecast.eval.awaiting_gate', {
              codes: (hb?.awaiting_gate ?? []).map((c) => t(`forecast.bench.${c}`)).join(t('forecast.listSeparator')),
            })}
          />
        )}
        {hb?.pass.last_error && <Alert type="error" showIcon title={hb.pass.last_error} />}
        {hb?.pass.waiting_for_budget && <Alert type="info" showIcon title={t('forecast.eval.waitingBudget')}
          description={hb.pass.resume_at ? t('forecast.eval.resumeAt', { at: dayjs(hb.pass.resume_at).format('YYYY-MM-DD HH:mm:ss') }) : undefined} />}

        <ForecastBacklog backlog={settings.backlog} />

        {hb && (
          <Typography.Text>
            {t('forecast.eval.summary', {
              resolved: hb.pass.resolved,
              due: hb.pass.due_unsettled,
              fetches,
              errors: hb.pass.errors + hb.pass.panics,
            })}
          </Typography.Text>
        )}

        {!on && (
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label={t('forecast.eval.readiness.title')}>
              <Space orientation="vertical" size={2}>
                {settings.readiness.map((r) => (
                  <Typography.Text key={r.key} type={r.ok ? undefined : 'danger'}>
                    {r.ok ? '✓' : '✗'} {t(`forecast.eval.readiness.${r.key}`)}
                    {!r.ok && readinessDetail(r) ? ` — ${readinessDetail(r)}` : ''}
                  </Typography.Text>
                ))}
              </Space>
            </Descriptions.Item>
          </Descriptions>
        )}

        {settings.mode === 'dry' && (
          <div>
            <Typography.Title level={5} style={{ marginTop: 0 }}>
              {t('forecast.eval.sample.title')}
            </Typography.Title>
            {hb && (hb.dry_sample?.length ?? 0) > 0 ? (
              <div className="rp-table-scroll">
              <Table
                size="small"
                rowKey={(r) => `${r.set_id}-${r.kind}-${r.mark ?? ''}-${r.code}`}
                pagination={false}
                dataSource={hb.dry_sample}
                columns={sampleColumns}
              />
              </div>
            ) : (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('forecast.eval.sample.empty')} />
            )}
          </div>
        )}

        {showGate && (
          <div>
            <Typography.Title level={5} style={{ marginTop: 0 }}>
              {t('forecast.eval.gate.title')}
            </Typography.Title>
            <Typography.Text type="secondary">{t('forecast.eval.gate.hint')}</Typography.Text>
            <Space wrap style={{ marginTop: 8 }}>
              <Select
                value={code}
                onChange={(next: string) => setPick({ code: next, against: suggested })}
                style={{ width: 160 }}
                aria-label={t('forecast.eval.gate.code')}
                options={gateCodes.map((c) => ({ value: c, label: t(`forecast.bench.${c}`) }))}
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
        )}
      </Space>
    </Card>
  )
}
