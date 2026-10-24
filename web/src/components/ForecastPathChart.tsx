import { useLayoutEffect, useState, type CSSProperties } from 'react'
import { theme, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { dayIndex, priceTicks, type ForecastPath, type ForecastPathCheckpoint } from '../lib/forecastPath'
import { fenToYuan } from '../lib/money'

// The forecast card's "forecast and actual" chart: the reported scenarios as straight reference lines
// from the starting close to the horizon, the actual closes since the start, and the settled
// checkpoints on top. Drawn by hand into one SVG for the same reason PriceChart is (no charting
// dependency for a line and a few dots).

interface Props {
  path: ForecastPath
  height?: number
}

// Used until the box reports a width (first paint, and test environments without layout).
const FALLBACK_WIDTH = 600
// The left margin holds the price labels.
const PAD = { left: 52, right: 6, top: 16, bottom: 18 }

export default function ForecastPathChart({ path, height = 220 }: Props) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const [box, setBox] = useState<HTMLDivElement | null>(null)
  const [width, setWidth] = useState(0)

  useLayoutEffect(() => {
    if (!box) return
    const apply = (raw: number) => {
      const next = Math.max(0, Math.round(raw))
      setWidth((prev) => (Math.abs(prev - next) >= 1 ? next : prev))
    }
    apply(box.getBoundingClientRect().width)
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver((entries) => apply(entries[0]?.contentRect.width ?? box.getBoundingClientRect().width))
    ro.observe(box)
    return () => ro.disconnect()
  }, [box])

  const w = width > 0 ? width : FALLBACK_WIDTH
  const h = height
  const span = dayIndex(path.end) - dayIndex(path.from)
  const round = (v: number) => Math.round(v * 10) / 10
  const x = (d: string) => round(PAD.left + ((dayIndex(d) - dayIndex(path.from)) / span) * (w - PAD.left - PAD.right))
  const y = (fen: number) => round(PAD.top + ((path.max - fen) / (path.max - path.min)) * (h - PAD.top - PAD.bottom))

  // Bear to bull in the scenario band's own colours: the page reads red as a rise, green as a fall.
  const scenarioColor = { bear: token.colorSuccess, base: token.colorTextTertiary, bull: token.colorError }
  // Each outcome is told apart by fill and ring, and each is seen at 3:1 or better against the card:
  // the warning fill alone is too light on white, so a disagreeing dot carries a dark ring.
  const dotStyle = (direction: ForecastPathCheckpoint['direction']) => {
    switch (direction) {
      case 'agree':
        return { fill: token.colorPrimary, stroke: token.colorBgContainer }
      case 'disagree':
        return { fill: token.colorWarning, stroke: token.colorTextSecondary }
      case 'flat':
        return { fill: token.colorTextTertiary, stroke: token.colorBgContainer }
      default:
        return { fill: token.colorBgContainer, stroke: token.colorTextTertiary }
    }
  }

  const x0 = x(path.from)
  const xEnd = x(path.end)
  const xTo = x(path.to)
  const y0 = y(path.s0)
  const last = path.actual[path.actual.length - 1]
  const label = t('forecast.path.aria', {
    from: path.from,
    to: path.to,
    s0: fenToYuan(path.s0),
    last: last ? fenToYuan(last.c) : '—',
  })
  const axisText = { fontSize: 11, fill: token.colorTextSecondary }
  const swatch = (color: string, dashed = false): CSSProperties => ({
    display: 'inline-block',
    width: 14,
    height: 0,
    borderTop: `2px ${dashed ? 'dashed' : 'solid'} ${color}`,
    verticalAlign: 'middle',
    marginInlineEnd: 4,
  })
  const directions = [...new Set(path.checkpoints.map((c) => c.direction ?? 'untested'))]

  return (
    <div data-testid="forecast-path">
      <div ref={setBox} style={{ width: '100%' }}>
        <svg role="img" aria-label={label} width="100%" height={h} viewBox={`0 0 ${w} ${h}`} style={{ display: 'block', overflow: 'visible' }}>
          {priceTicks(path.min, path.max).map((fen) => (
            <g key={fen}>
              <line x1={PAD.left} y1={y(fen)} x2={w - PAD.right} y2={y(fen)} stroke={token.colorBorderSecondary} />
              <text data-testid="forecast-path-tick" x={PAD.left - 6} y={y(fen) + 4} textAnchor="end" {...axisText}>{fenToYuan(fen)}</text>
            </g>
          ))}
          {/* The starting close, across the whole window, for reading the actual line against. */}
          <line x1={x0} y1={y0} x2={xEnd} y2={y0} stroke={token.colorBorder} strokeDasharray="2 3" />
          {path.today && (
            <g data-testid="forecast-path-today">
              <line x1={x(path.today)} y1={PAD.top - 4} x2={x(path.today)} y2={h - PAD.bottom} stroke={token.colorBorder} />
              <text x={x(path.today)} y={PAD.top - 6} textAnchor="middle" {...axisText}>{t('forecast.path.today')}</text>
            </g>
          )}
          {path.scenarios.map((s) => (
            <line key={s.label} data-testid="forecast-path-scenario" data-label={s.label} x1={x0} y1={y0} x2={xTo} y2={y(s.fen)} stroke={scenarioColor[s.label]} strokeWidth={1.5} />
          ))}
          {path.terminal !== undefined && (
            <line data-testid="forecast-path-terminal" x1={x0} y1={y0} x2={xTo} y2={y(path.terminal)} stroke={token.colorPrimary} strokeWidth={1.5} strokeDasharray="5 4" />
          )}
          {path.actual.length >= 2 && (
            <polyline
              data-testid="forecast-path-actual"
              points={path.actual.map((b) => `${x(b.d)},${y(b.c)}`).join(' ')}
              fill="none"
              stroke={token.colorText}
              strokeWidth={2}
              strokeLinejoin="round"
            />
          )}
          <circle cx={x0} cy={y0} r={3} fill={token.colorText} />
          {path.checkpoints.map((c) => (
            <circle key={c.checkpoint} data-testid="forecast-path-checkpoint" data-direction={c.direction ?? 'untested'} cx={x(c.d)} cy={y(c.fen)} r={4.5} strokeWidth={1.5} {...dotStyle(c.direction)}>
              <title>{[t(`forecast.cp.${c.checkpoint}`), c.d, fenToYuan(c.fen), t(`forecast.direction.${c.direction ?? 'untested'}`)].join(' · ')}</title>
            </circle>
          ))}
          <text x={x0} y={h - 4} textAnchor="start" {...axisText}>{path.from}</text>
          <text x={xTo} y={h - 4} textAnchor="end" {...axisText}>{path.to}</text>
        </svg>
      </div>
      <div data-testid="forecast-path-legend" style={{ display: 'flex', flexWrap: 'wrap', gap: '4px 16px', marginTop: 6, fontSize: 12 }}>
        <Typography.Text style={{ fontSize: 12 }}><span style={swatch(token.colorText)} />{t('forecast.path.actual')}</Typography.Text>
        {[...path.scenarios].reverse().map((s) => (
          <Typography.Text key={s.label} style={{ fontSize: 12 }}><span style={swatch(scenarioColor[s.label])} />{`${t(`forecast.panel.${s.label}`)} ${fenToYuan(s.fen)}`}</Typography.Text>
        ))}
        {path.terminal !== undefined && (
          <Typography.Text style={{ fontSize: 12 }}><span style={swatch(token.colorPrimary, true)} />{`${t('forecast.panel.terminal')} ${fenToYuan(path.terminal)}`}</Typography.Text>
        )}
        {directions.map((dir) => {
          const style = dotStyle(dir === 'untested' ? undefined : dir)
          return (
            <Typography.Text key={dir} style={{ fontSize: 12 }}>
              <span style={{ display: 'inline-block', width: 9, height: 9, borderRadius: '50%', background: style.fill, border: `1.5px solid ${style.stroke === token.colorBgContainer ? style.fill : style.stroke}`, verticalAlign: 'middle', marginInlineEnd: 4 }} />
              {`${t('forecast.path.checkpoint')} · ${t(`forecast.direction.${dir}`)}`}
            </Typography.Text>
          )
        })}
      </div>
      {path.corpAction && (
        <Typography.Paragraph type="warning" style={{ fontSize: 12, margin: '6px 0 0' }}>{t('forecast.path.corpAction')}</Typography.Paragraph>
      )}
      <Typography.Paragraph style={{ color: token.colorTextSecondary, fontSize: 12, margin: '6px 0 0' }}>{t('forecast.path.note')}</Typography.Paragraph>
    </div>
  )
}
