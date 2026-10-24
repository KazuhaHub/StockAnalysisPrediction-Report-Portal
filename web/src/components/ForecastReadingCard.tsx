import { useEffect, useState } from 'react'
import { Alert, Button, Card, Descriptions, Skeleton, Space, Steps, Table, Tag, Typography, theme } from 'antd'
import { DownOutlined, UpOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import type { ForecastReadingEdition, PredictionClaim, PredictionMark, PredictionSet } from '../api/types'
import { fenToYuan } from '../lib/money'
import { readingSet } from '../lib/forecastReading'
import ScrollRegion from './forecast/ScrollRegion'
import { formatReportDateTime, isInstant } from '../lib/datetime'
import { forecastLinkColor, forecastPalette } from '../lib/forecastPresentation'
import WarnTag from './forecast/WarnTag'

// The report's own prediction panel (plan §8.2). It is part of the READING page, not an admin view:
// a reader is looking at what this report promised, and whether the market has answered yet.
//
// Display selection follows the current report body; monthly membership is a separate annotation.
// An absent or rejected set has no claims to show. Its report gets the explicit empty state instead.

const OPEN_KEY = 'rp_forecast_panel_open'

function readOpen(): boolean {
  try {
    return window.localStorage.getItem(OPEN_KEY) !== '0'
  } catch {
    return true
  }
}

const CHECKPOINTS = ['d5', 'd20', 'd60', 'd120', 'h12'] as const

// The checkpoints the ledger actually schedules for this set. h12 exists only for a standard 12-month
// horizon: the server never creates that mark otherwise (predMarkCheckpoints), so a row or step for it
// would wait forever for a settlement that cannot happen.
function checkpointsFor(s: PredictionSet): readonly (typeof CHECKPOINTS)[number][] {
  return CHECKPOINTS.filter((cp) => cp !== 'h12' || s.horizon_std)
}

function horizonText(s: PredictionSet, t: TFunction): string {
  if (!s.horizon_std) return s.horizon_end || '—'
  return s.horizon_end
    ? t('forecast.reading.horizon_until_standard', { date: s.horizon_end, months: 12 })
    : t('forecast.reading.standard_horizon', { months: 12 })
}

function claim(s: PredictionSet, kind: string, label?: string): PredictionClaim | undefined {
  return s.claims.find((c) => c.kind === kind && (label === undefined || c.label === label))
}

function signedBP(value?: number): string {
  if (value === undefined) return '—'
  return `${value > 0 ? '+' : ''}${(value / 100).toFixed(2)}%`
}

interface Props {
  sets: PredictionSet[]
  countedSetID?: number
  displaySetID?: number
  settlementPaused?: boolean
  otherSets: PredictionSet[]
  latestClose?: number
  latestDate?: string
  symbol: string
  reportDate?: string
  receivedAt?: string
  edition?: string
  sibling?: ForecastReadingEdition
  countedEdition?: ForecastReadingEdition
  loading?: boolean
  allowPayload?: boolean
  allowMembership?: boolean
  allowOrphans?: boolean
  reportKind?: string
  // Whether forecast verification covers this report's type, as the server answered it. Only an
  // explicit false hides the empty card; an older server that omits it keeps the card.
  covered?: boolean
}

// A small card's 12px inset left the content against the border; the report card above uses the
// standard 24px sides, so this one matches it while keeping the small card's vertical rhythm.
const CARD_STYLES = { header: { paddingInline: 24 }, body: { padding: '16px 24px' } }

export default function ForecastReadingCard({
  sets,
  countedSetID,
  displaySetID,
  settlementPaused,
  otherSets,
  latestClose,
  latestDate,
  symbol,
  reportDate,
  receivedAt,
  edition,
  sibling,
  countedEdition,
  loading = false,
  allowPayload = true,
  allowMembership = true,
  allowOrphans = true,
  reportKind = '',
  covered,
}: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  // Bear to bull reads as fall to rise, in the page's own colours (QuoteStrip: red for a rise, green
  // for a fall). The level backgrounds the band used before measured about 1.1:1 against the card in
  // both themes, which is why it could not be seen; these stops clear 2:1 in both.
  const bandStops = [token.colorSuccess, token.colorTextTertiary, token.colorError]
  const palette = forecastPalette(token)
  const [open, setOpen] = useState(readOpen)

  // A test run is listed here too (P36), so a producer's console run is visibly a test rather than
  // vanishing. It has no marks and no checkpoint table; only production sets are scored. An accepted
  // set always wins the panel when there is one — a test set must never shadow the record.
  const set = readingSet(sets, displaySetID)
  useEffect(() => {
    if (loading || window.location.hash !== '#forecast') return
    const frame = requestAnimationFrame(() => document.getElementById('forecast')?.scrollIntoView?.({ block: 'start' }))
    return () => cancelAnimationFrame(frame)
  }, [loading, set?.id])
  const anchorStyle = { marginTop: 12, scrollMarginTop: 'calc(var(--rp-header-h, 64px) + 16px)' }
  if (loading) {
    return <Card id="forecast" className="rp-forecast-panel" styles={CARD_STYLES} size="small" title={t('forecast.panel.title')} style={anchorStyle}><Skeleton active /></Card>
  }
  if (!set) {
    // A report type the ledger never covers has no forecast to be missing. The empty state belongs to a
    // covered report that made none.
    if (covered === false) return null
    return (
      <Card id="forecast" className="rp-forecast-panel" styles={CARD_STYLES} size="small" title={t('forecast.panel.title')} style={anchorStyle}>
        <Typography.Text>{t('forecast.reading.no_forecast')}</Typography.Text>
        {sibling && <div style={{ marginTop: 8 }}><Link to={sibling.url}>{t('forecast.reading.other_edition')}</Link></div>}
        <Typography.Paragraph style={{ color: token.colorTextSecondary, margin: '12px 0 0' }}>{t('forecast.disclaimer')}</Typography.Paragraph>
      </Card>
    )
  }
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

  const stanceClaim = claim(set, 'stance')
  const stance = stanceClaim?.stance
  const terminalClaim = claim(set, 'terminal_value')
  const terminal = terminalClaim?.price_fen
  const pProfit = terminalClaim?.prob_bp
  const entry = claim(set, 'entry_price')?.price_fen
  const scenarios = (['bear', 'base', 'bull'] as const).map((label) => ({ label, c: claim(set, 'scenario', label) }))
  const upside = terminal !== undefined && set.s0_fen ? terminal / set.s0_fen - 1 : undefined
  const prices = scenarios.map(({ c }) => c?.price_fen).filter((price): price is number => price !== undefined)
  const min = prices.length ? Math.min(...prices) : undefined
  const max = prices.length ? Math.max(...prices) : undefined
  const cursor = latestClose !== undefined && min !== undefined && max !== undefined && max > min
    ? Math.max(0, Math.min(100, (latestClose - min) / (max - min) * 100)) : undefined
  const others = otherSets.filter((other) => other.id !== set.id && other.report_id !== set.report_id && (allowOrphans || other.report_id))
  const returnColor = (value: string) => value.startsWith('-') ? palette.negative.foreground : value.startsWith('+') ? palette.positive.foreground : undefined

  const badges = (
    <Space size={[4, 4]} wrap>
      {isTest && <Tag color="purple">{t('forecast.panel.test_run')}</Tag>}
      <Tag style={{ color: token.colorText, background: set.baseline_check === 'match' ? token.colorSuccessBg : set.baseline_check === 'mismatch' ? token.colorErrorBg : undefined }}>
        {set.baseline_check === 'match' && set.baseline_emitted_fen !== undefined && set.baseline_emitted_fen === set.s0_fen
          ? t('forecast.reading.reported_close', {
              reported: fenToYuan(set.baseline_emitted_fen),
              close: fenToYuan(set.s0_fen),
              date: set.s0_date || set.baseline_as_of || '—',
            })
          : t(`forecast.panel.baseline.${set.baseline_check}`)}
      </Tag>
      {set.decision_quality && <Tag>{t(`forecast.quality.${set.decision_quality}`)}</Tag>}
      {set.probability_method === 'approved_policy_default' && (
        <WarnTag>{t('forecast.panel.default_probability')}</WarnTag>
      )}
      {allowMembership && !isTest && (set.membership === 'member' || set.id === countedSetID) && <Tag style={{ color: token.colorText, background: token.colorInfoBg }}>{t('forecast.reading.counted')}</Tag>}
      {allowMembership && !isTest && set.membership && set.membership !== 'member' && !countedEdition && set.id !== countedSetID && <Tag>{t(`forecast.membership.${set.membership}`)}</Tag>}
      {allowMembership && countedEdition && countedEdition.report_id !== set.report_id && (
        <Tag><Link style={{ color: token.colorText, textDecoration: 'underline' }} to={countedEdition.url}>{t('forecast.reading.counted_edition', { edition: countedEdition.version })}</Link></Tag>
      )}
    </Space>
  )

  // A pending mark is not a failure. The server sends the phase and the earliest date it can settle
  // (an exchange-backed estimate or weekday lower bound); the panel only chooses the words.
  const phaseText = (m: PredictionMark) => {
    const onOrAfter = (date?: string) =>
      date ? t(m.expected ? 'forecast.phase.expected' : 'forecast.phase.on_or_after', { date }) : t('forecast.phase.waiting')
    switch (m.phase) {
      case 'waiting_s0':
        return `${t('forecast.phase.waiting_s0')}${m.not_before ? ` · ${onOrAfter(m.not_before)}` : ''}`
      case 'waiting':
        return onOrAfter(m.not_before)
      case 'retrying':
        return t('forecast.phase.retrying')
      case 'settling':
        return t('forecast.phase.settling')
      case 'overdue':
        return t('forecast.phase.overdue')
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
    const excess = resolved && m.ret_bp !== undefined && m.bench_ret_bp !== undefined ? m.ret_bp - m.bench_ret_bp : undefined
    const touched =
      resolved && entry !== undefined && m.low_fen !== undefined ? m.low_fen <= entry : undefined
    const agree = resolved ? m.direction : undefined
    return {
      key: m.checkpoint,
      checkpoint: t(`forecast.cp.${m.checkpoint}`),
      status: m.halted ? `${phaseText(m)} · ${t('forecast.mark.halted')}` : phaseText(m),
      date: resolved ? m.bar_date || m.session_date || '—' : '—',
      close: !resolved || m.close_fen === undefined ? '—' : fenToYuan(m.close_fen),
      ret: signedBP(resolved ? m.ret_bp : undefined),
      bench: signedBP(resolved ? m.bench_ret_bp : undefined),
      excess: signedBP(excess),
      touched: touched === undefined ? '—' : touched ? t('forecast.panel.yes') : t('forecast.panel.no'),
      agree: agree === undefined
        ? '—'
        : typeof agree === 'string'
          ? t(`forecast.direction.${agree}`)
          : agree ? t('forecast.panel.agree') : t('forecast.panel.disagree'),
      resolved,
      source: resolved ? m.source || '—' : '—',
      low: m.low_fen,
    }
  }
  const marks: PredictionMark[] = checkpointsFor(set).map((cp) => set.marks.find((m) => m.checkpoint === cp) ?? {
    checkpoint: cp, status: 'pending', phase: 'waiting',
  })

  return (
    <Card
      id="forecast"
      className="rp-forecast-panel" styles={CARD_STYLES}
      size="small"
      title={t('forecast.panel.title')}
      extra={
        <Button type="link" size="small" onClick={toggle} aria-expanded={open} style={{ color: forecastLinkColor(token) }}>
          {open ? <UpOutlined /> : <DownOutlined />} {open ? t('forecast.panel.collapse') : t('forecast.panel.expand')}
        </Button>
      }
      style={anchorStyle}
    >
      {open && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {set.body_changed && <Alert type="warning" showIcon title={t('forecast.panel.body_changed')} />}
          {settlementPaused && !isTest && <Alert type="info" showIcon title={t('forecast.phase.settlement_paused')} />}
          {badges}

          {(reportDate || receivedAt || edition) && (
            <Typography.Text style={{ color: token.colorTextSecondary }}>
              {t('forecast.reading.meta', { date: reportDate || '—', received: isInstant(receivedAt) ? formatReportDateTime(receivedAt) : receivedAt || '—', edition: edition || set.version || '—' })}
            </Typography.Text>
          )}

          <Typography.Text strong>{t('forecast.reading.what_said')}</Typography.Text>

          <Descriptions size="small" column={1} bordered styles={{ label: { color: token.colorTextSecondary } }}>
            <Descriptions.Item label={t('forecast.panel.stance')}>
              {stance === 'long' ? t('forecast.stance.long') : t('forecast.stance.no_position')}
              {stanceClaim?.weight_bp !== undefined && ` · ${(stanceClaim.weight_bp / 100).toFixed(0)}%`}
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.panel.horizon')}>
              {horizonText(set, t)}
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.panel.terminal')}>
              {terminal === undefined ? '—' : fenToYuan(terminal)}
              {set.s0_fen !== undefined && (
                <Typography.Text style={{ color: token.colorTextSecondary }}>
                  {' '}
                  · {t('forecast.panel.s0')} {fenToYuan(set.s0_fen)}
                </Typography.Text>
              )}
              {upside !== undefined && (
                <Tag style={{ marginInlineStart: 8 }}>{`${upside > 0 ? '+' : ''}${(upside * 100).toFixed(1)}%`}</Tag>
              )}
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.panel.p_profit')}>
              {pProfit === undefined ? '—' : `${(pProfit / 100).toFixed(0)}%`}
            </Descriptions.Item>
            <Descriptions.Item label={t('forecast.panel.entry')}>
              {entry === undefined ? '—' : fenToYuan(entry)}
              <Typography.Text style={{ color: token.colorTextSecondary }}> · {t('forecast.panel.entry_derived')}</Typography.Text>
            </Descriptions.Item>
          </Descriptions>

          <div role="group" aria-label={t('forecast.panel.scenarios')} style={{ padding: '32px 12px 4px' }}>
            {/* The marker is positioned against the band's own box, so its percentage is a point on the
                band rather than on the padded group. The tick stays on that point; only the label is
                pulled inward near either end, so it runs at most 6px past the band, inside the padding. */}
            <div style={{ position: 'relative' }}>
              <div
                data-testid="forecast-scenario-band"
                data-stops={JSON.stringify(bandStops)}
                style={{ height: 8, borderRadius: 8, background: `linear-gradient(90deg, ${bandStops.join(', ')})` }}
              />
              {cursor !== undefined && (
                <div data-testid="forecast-scenario-cursor" data-position={cursor} style={{ position: 'absolute', insetBlockStart: 0, insetInlineStart: `${cursor}%`, width: 0, height: 8 }}>
                  <span
                    data-testid="forecast-scenario-cursor-label"
                    style={{
                      position: 'absolute',
                      insetBlockEnd: 'calc(100% + 6px)',
                      insetInlineStart: 0,
                      whiteSpace: 'nowrap',
                      transform: cursor < 15 ? 'translateX(-6px)' : cursor > 85 ? 'translateX(calc(-100% + 6px))' : 'translateX(-50%)',
                    }}
                  >
                    <Tag style={{ marginInlineEnd: 0 }}>{fenToYuan(latestClose!)}</Tag>
                  </span>
                  <span
                    data-testid="forecast-scenario-cursor-tick"
                    style={{
                      position: 'absolute',
                      insetBlockStart: -6,
                      insetInlineStart: -1,
                      width: 2,
                      height: 18,
                      borderRadius: 1,
                      background: token.colorText,
                      // A hairline of the card colour keeps the tick distinct where it crosses the band.
                      boxShadow: `0 0 0 1px ${token.colorBgContainer}`,
                    }}
                  />
                </div>
              )}
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8, marginTop: 6 }}>
              {scenarios.map(({ label, c }) => (
                <Typography.Text key={label} style={{ fontSize: 12 }}>
                  {t(`forecast.panel.${label}`)} {c?.price_fen === undefined ? '—' : fenToYuan(c.price_fen)}
                  {c?.prob_bp !== undefined && ` · ${(c.prob_bp / 100).toFixed(0)}%`}
                </Typography.Text>
              ))}
            </div>
          </div>

          {!isTest && (
            <div>
              <Typography.Text strong>{t('forecast.panel.checkpoints')}</Typography.Text>
              <Steps
                size="small"
                responsive
                style={{ marginTop: 12 }}
                styles={{ itemTitle: { color: token.colorTextSecondary } }}
                items={marks.map((m) => ({
                  title: t(`forecast.cp.${m.checkpoint}`),
                  status: m.status === 'resolved' ? 'finish' : m.phase === 'final' ? 'error' : m.phase === 'settling' || m.phase === 'retrying' || m.phase === 'overdue' ? 'process' : 'wait',
                }))}
              />
              <ScrollRegion label={t('forecast.panel.checkpoints')}><Table
                size="small"
                rowKey="key"
                pagination={false}
                style={{ marginTop: 8, minWidth: 1100 }}
                dataSource={marks.map(markRow)}
                columns={[
                  { title: t('forecast.panel.mark'), dataIndex: 'checkpoint', key: 'checkpoint' },
                  { title: t('forecast.panel.mark_status'), dataIndex: 'status', key: 'status' },
                  { title: t('forecast.panel.mark_date'), dataIndex: 'date', key: 'date' },
                  { title: t('forecast.panel.close'), dataIndex: 'close', key: 'close' },
                  { title: t('forecast.panel.ret'), dataIndex: 'ret', key: 'ret', render: (v: string) => <span style={{ color: returnColor(v) }}>{v}</span> },
                  { title: t('forecast.panel.bench_ret'), dataIndex: 'bench', key: 'bench', render: (v: string) => <span style={{ color: returnColor(v) }}>{v}</span> },
                  { title: t('forecast.panel.excess'), dataIndex: 'excess', key: 'excess', render: (v: string) => <strong style={{ color: returnColor(v) }}>{v}</strong> },
                  { title: t('forecast.panel.touched'), dataIndex: 'touched', key: 'touched', render: (v: string, row) => row.low === undefined || v === '—' ? v : v === t('forecast.panel.yes') ? t('forecast.reading.touched') : t('forecast.reading.not_touched', { low: fenToYuan(row.low) }) },
                  { title: t('forecast.panel.agreement'), dataIndex: 'agree', key: 'agree' },
                  { title: t('forecast.reading.source'), dataIndex: 'source', key: 'source' },
                ]}
              /></ScrollRegion>
              <Typography.Text style={{ color: token.colorTextSecondary, fontSize: 12 }}>
                {t('forecast.panel.settle_note')}
              </Typography.Text>
            </div>
          )}

          {latestClose !== undefined && (
            <Typography.Text style={{ color: token.colorTextSecondary }}>
              {t('forecast.panel.latest_close', { date: latestDate ?? '', price: fenToYuan(latestClose) })}
            </Typography.Text>
          )}

          {others.length > 0 && (
            <div>
              <Typography.Text strong>{t('forecast.panel.others')}</Typography.Text>
              <ScrollRegion label={t('forecast.panel.others')}><Table
                size="small" pagination={false} rowKey="id" dataSource={others} style={{ minWidth: 550 }}
                onRow={(other) => ({ style: { color: other.report_id ? undefined : token.colorTextSecondary } })}
                columns={[
                  { title: t('forecast.reading.report_date'), key: 'date', render: (_, other) => other.report_id ? <Link style={{ color: forecastLinkColor(token) }} to={`/stock/${encodeURIComponent(symbol)}?date=${encodeURIComponent(other.report_date || other.made_on)}&kind=${encodeURIComponent(other.report_kind || reportKind)}&r=${other.report_id}#forecast`}>{other.report_date || other.made_on}</Link> : other.report_date || other.made_on },
                  { title: t('forecast.reading.edition'), dataIndex: 'version' },
                  { title: t('forecast.panel.stance'), key: 'stance', render: (_, other) => { const value = claim(other, 'stance')?.stance; return value ? t(`forecast.stance.${value}`) : '—' } },
                  { title: t('forecast.panel.mark_status'), key: 'state', render: (_, other) => t(`forecast.state.${other.state}`) },
                ]}
              /></ScrollRegion>
            </div>
          )}

          {allowPayload && set.payload && (
            <details>
              <summary style={{ cursor: 'pointer' }}>{t('forecast.panel.payload')}</summary>
              <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word', fontSize: 12 }}>{set.payload}</pre>
            </details>
          )}
        </div>
      )}
      <Typography.Paragraph style={{ color: token.colorTextSecondary, margin: '12px 0 0' }}>{t('forecast.disclaimer')}</Typography.Paragraph>
    </Card>
  )
}
