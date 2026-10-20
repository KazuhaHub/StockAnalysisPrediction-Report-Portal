import { useState } from 'react'
import { Alert, Button, Card, Descriptions, Space, Table, Tag, Typography } from 'antd'
import { DownOutlined, UpOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { PredictionClaim, PredictionMark, PredictionSet } from '../api/types'
import { fenToYuan } from '../lib/money'

// The report's own prediction panel (plan §8.2). It is part of the READING page, not an admin view:
// a reader is looking at what this report promised, and whether the market has answered yet.
//
// Only a set with state=accepted has numbers. An absent or rejected set renders nothing here — the
// ingest row exists for coverage accounting, not as a claim to show a reader.

const OPEN_KEY = 'rp_forecast_panel_open'

function readOpen(): boolean {
  try {
    return window.localStorage.getItem(OPEN_KEY) !== '0'
  } catch {
    return true
  }
}

const CHECKPOINTS = ['d5', 'd20', 'd60', 'd120', 'h12'] as const

function claim(s: PredictionSet, kind: string, label?: string): PredictionClaim | undefined {
  return s.claims.find((c) => c.kind === kind && (label === undefined || c.label === label))
}

function pctChange(numerator?: number, denominator?: number): string {
  if (numerator === undefined || denominator === undefined || denominator <= 0) return '—'
  return `${((numerator / denominator - 1) * 100).toFixed(1)}%`
}

interface Props {
  sets: PredictionSet[]
  countedSetID?: number
  otherSets: PredictionSet[]
  latestClose?: number
  latestDate?: string
  symbol: string
}

export default function PredictionPanel({ sets, countedSetID, otherSets, latestClose, latestDate, symbol }: Props) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(readOpen)

  // A test run is listed here too (P36), so a producer's console run is visibly a test rather than
  // vanishing. It has no marks and no checkpoint table; only production sets are scored. An accepted
  // set always wins the panel when there is one — a test set must never shadow the record.
  const accepted = sets.filter((s) => s.state === 'accepted')
  const tests = sets.filter((s) => s.state === 'test')
  const set = accepted.find((s) => s.id === countedSetID) ?? accepted[accepted.length - 1] ?? tests[tests.length - 1]
  if (!set) return null
  const isTest = set.state === 'test'

  const toggle = () => {
    const next = !open
    setOpen(next)
    try {
      window.localStorage.setItem(OPEN_KEY, next ? '1' : '0')
    } catch {
      /* storage blocked — the choice holds for this page view */
    }
  }

  const stance = claim(set, 'stance')?.stance
  const terminalClaim = claim(set, 'terminal_value')
  const terminal = terminalClaim?.price_fen
  const pProfit = terminalClaim?.prob_bp
  const entry = claim(set, 'entry_price')?.price_fen
  const scenarios = (['bear', 'base', 'bull'] as const).map((label) => ({ label, c: claim(set, 'scenario', label) }))
  const upside = terminal !== undefined && set.s0_fen ? terminal / set.s0_fen - 1 : undefined

  const badges = (
    <Space size={[4, 4]} wrap>
      {isTest && <Tag color="purple">{t('forecast.panel.test_run')}</Tag>}
      <Tag color={set.baseline_check === 'match' ? 'green' : set.baseline_check === 'mismatch' ? 'red' : 'default'}>
        {t(`forecast.panel.baseline.${set.baseline_check}`)}
      </Tag>
      {/* The counted/not-counted badge is deliberately absent until real membership ships: the
          reading page had no membership data, so every set said "not counted" — a false claim. */}
      {set.decision_quality && <Tag>{t(`forecast.quality.${set.decision_quality}`)}</Tag>}
      {set.probability_method === 'approved_policy_default' && (
        <Tag color="orange">{t('forecast.panel.default_probability')}</Tag>
      )}
    </Space>
  )

  // A pending mark is not a failure. The server sends the phase and the earliest date it can settle
  // (a weekday projection); the panel only chooses the words.
  const phaseText = (m: PredictionMark) => {
    const onOrAfter = (date?: string) =>
      date ? t('forecast.phase.on_or_after', { date }) : t('forecast.phase.waiting')
    switch (m.phase) {
      case 'waiting_s0':
        return `${t('forecast.phase.waiting_s0')}${m.not_before ? ` · ${onOrAfter(m.not_before)}` : ''}`
      case 'waiting':
        return onOrAfter(m.not_before)
      case 'retrying':
        return t('forecast.phase.retrying')
      case 'resolved':
        return t('forecast.phase.resolved')
      case 'final':
        return t('forecast.phase.final')
      default:
        return m.status === 'resolved' ? t('forecast.phase.resolved') : t('forecast.phase.waiting')
    }
  }

  const markRow = (m: PredictionMark) => {
    const resolved = m.status === 'resolved'
    const excess = m.ret_bp !== undefined && m.bench_ret_bp !== undefined ? m.ret_bp - m.bench_ret_bp : undefined
    const touched =
      resolved && entry !== undefined && m.low_fen !== undefined ? m.low_fen <= entry : undefined
    const agree =
      resolved && excess !== undefined && upside !== undefined
        ? (excess > 0 && upside > 0) || (excess < 0 && upside < 0)
        : undefined
    return {
      key: m.checkpoint,
      checkpoint: t(`forecast.cp.${m.checkpoint}`),
      status: m.halted ? `${phaseText(m)} · ${t('forecast.mark.halted')}` : phaseText(m),
      date: m.bar_date || m.session_date || '—',
      close: m.close_fen === undefined ? '—' : fenToYuan(m.close_fen),
      ret: m.ret_bp === undefined ? '—' : `${(m.ret_bp / 100).toFixed(2)}%`,
      bench: m.bench_ret_bp === undefined ? '—' : `${(m.bench_ret_bp / 100).toFixed(2)}%`,
      excess: excess === undefined ? '—' : `${(excess / 100).toFixed(2)}%`,
      touched: touched === undefined ? '—' : touched ? t('forecast.panel.yes') : t('forecast.panel.no'),
      agree: agree === undefined ? '—' : agree ? t('forecast.panel.agree') : t('forecast.panel.disagree'),
      resolved,
    }
  }
  const marks = CHECKPOINTS.map((cp) => set.marks.find((m) => m.checkpoint === cp)).filter(
    (m): m is PredictionMark => m !== undefined,
  )

  return (
    <Card
      className="rp-forecast-panel"
      size="small"
      title={t('forecast.panel.title')}
      extra={
        <Button type="link" size="small" onClick={toggle} aria-expanded={open}>
          {open ? <UpOutlined /> : <DownOutlined />} {open ? t('forecast.panel.collapse') : t('forecast.panel.expand')}
        </Button>
      }
      style={{ marginTop: 12 }}
    >
      {open && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {set.body_changed && <Alert type="warning" showIcon title={t('forecast.panel.body_changed')} />}
          {badges}

          <Descriptions size="small" column={1} bordered>
            <Descriptions.Item label={t('forecast.panel.stance')}>
              {stance === 'long' ? t('forecast.stance.long') : t('forecast.stance.no_position')}
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.panel.horizon')}>{set.horizon_end || '—'}</Descriptions.Item>
            <Descriptions.Item label={t('forecast.panel.terminal')}>
              {terminal === undefined ? '—' : fenToYuan(terminal)}
              {set.s0_fen !== undefined && (
                <Typography.Text type="secondary">
                  {' '}
                  · {t('forecast.panel.s0')} {fenToYuan(set.s0_fen)}
                </Typography.Text>
              )}
              {upside !== undefined && (
                <Tag style={{ marginInlineStart: 8 }}>{`${(upside * 100).toFixed(1)}%`}</Tag>
              )}
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.panel.p_profit')}>
              {pProfit === undefined ? '—' : `${(pProfit / 100).toFixed(0)}%`}
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.panel.entry')}>
              {entry === undefined ? '—' : fenToYuan(entry)}
              <Typography.Text type="secondary"> · {t('forecast.panel.entry_derived')}</Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.panel.scenarios')}>
              <Space wrap>
                {scenarios.map(({ label, c }) => (
                  <Tag key={label}>
                    {t(`forecast.panel.${label}`)} {c?.price_fen === undefined ? '—' : fenToYuan(c.price_fen)}
                    {c?.prob_bp !== undefined && ` · ${(c.prob_bp / 100).toFixed(0)}%`}
                  </Tag>
                ))}
              </Space>
            </Descriptions.Item>
          </Descriptions>

          {!isTest && (
            <div>
              <Typography.Text strong>{t('forecast.panel.checkpoints')}</Typography.Text>
              <Table
                size="small"
                rowKey="key"
                pagination={false}
                style={{ marginTop: 8 }}
                dataSource={marks.map(markRow)}
                columns={[
                  { title: t('forecast.panel.mark'), dataIndex: 'checkpoint', key: 'checkpoint' },
                  { title: t('forecast.panel.mark_status'), dataIndex: 'status', key: 'status' },
                  { title: t('forecast.panel.mark_date'), dataIndex: 'date', key: 'date' },
                  { title: t('forecast.panel.close'), dataIndex: 'close', key: 'close' },
                  { title: t('forecast.panel.ret'), dataIndex: 'ret', key: 'ret' },
                  { title: t('forecast.panel.bench_ret'), dataIndex: 'bench', key: 'bench' },
                  { title: t('forecast.panel.excess'), dataIndex: 'excess', key: 'excess' },
                  { title: t('forecast.panel.touched'), dataIndex: 'touched', key: 'touched' },
                  { title: t('forecast.panel.agreement'), dataIndex: 'agree', key: 'agree' },
                ]}
              />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('forecast.panel.settle_note')}
              </Typography.Text>
            </div>
          )}

          {latestClose !== undefined && (
            <Typography.Text type="secondary">
              {t('forecast.panel.latest_close', { date: latestDate ?? '', price: fenToYuan(latestClose) })}
            </Typography.Text>
          )}

          {otherSets.filter((s) => s.report_id !== set.report_id).length > 0 && (
            <div>
              <Typography.Text strong>{t('forecast.panel.others')}</Typography.Text>
              <ul style={{ margin: '6px 0 0', paddingInlineStart: 20 }}>
                {otherSets
                  .filter((s) => s.report_id !== set.report_id && s.id !== set.id)
                  .map((s) => (
                    <li key={s.id}>
                      {s.report_id ? (
                        <Link to={`/stock/${encodeURIComponent(symbol)}?date=${encodeURIComponent(s.made_on)}`}>
                          {s.made_on}
                        </Link>
                      ) : (
                        s.made_on
                      )}{' '}
                      · {t(`forecast.state.${s.state}`)}
                      {s.state === 'accepted' && claim(s, 'stance')?.stance
                        ? ` · ${t(`forecast.stance.${claim(s, 'stance')?.stance}`)}`
                        : ''}
                    </li>
                  ))}
              </ul>
            </div>
          )}

          {set.payload && (
            <details>
              <summary style={{ cursor: 'pointer' }}>{t('forecast.panel.payload')}</summary>
              <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word', fontSize: 12 }}>{set.payload}</pre>
            </details>
          )}
        </div>
      )}
    </Card>
  )
}
