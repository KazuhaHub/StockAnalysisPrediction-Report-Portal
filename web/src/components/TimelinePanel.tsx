import { Timeline, Typography, theme } from 'antd'
import type { TimelineNode } from '../api/types'
import { clickable } from '../lib/clickable'
import { useTranslation } from 'react-i18next'

interface Props {
  nodes: TimelineNode[]
  selected: string
  onSelect: (date: string) => void
  // horizontal = mobile: a compact, horizontally-scrollable strip of date chips.
  // vertical = desktop: the classic antd Timeline inside a bounded scroll box.
  horizontal: boolean
}

export default function TimelinePanel({ nodes, selected, onSelect, horizontal }: Props) {
  const { token } = theme.useToken()
  const { t } = useTranslation()

  if (horizontal) {
    return (
      <div
        data-testid="timeline-hscroll"
        style={{ display: 'flex', gap: 8, overflowX: 'auto', padding: '2px 0 6px', WebkitOverflowScrolling: 'touch' }}
      >
        {nodes.map((n) => {
          const active = n.date === selected
          return (
            <button
              key={n.date}
              type="button"
              aria-label={`${n.date}, ${n.n}${n.predicted ? `, ${t('forecast.timeline.predicted')}` : ''}`}
              onClick={() => onSelect(n.date)}
              style={{
                flex: '0 0 auto',
                cursor: 'pointer',
                borderRadius: 16,
                padding: '4px 12px',
                fontSize: 13,
                lineHeight: 1.5,
                border: `1px solid ${active ? token.colorPrimary : token.colorBorder}`,
                background: active ? token.colorPrimary : token.colorBgContainer,
                color: active ? token.colorTextLightSolid : token.colorText,
              }}
            >
              <span>{n.date}</span>
              <span style={{ marginLeft: 6, fontSize: 12, opacity: 0.75 }}>{n.n}</span>
              {/* The selected chip is filled with the primary colour, so its dot takes the chip's own text colour. */}
              {n.predicted && <span aria-hidden="true" style={{ marginLeft: 6, color: active ? token.colorTextLightSolid : token.colorPrimary }}>●</span>}
            </button>
          )
        })}
      </div>
    )
  }

  return (
    // Bounded height + internal scroll: a very long history never stretches the page.
    // paddingTop keeps the first node's dot from being clipped by the scroll edge.
    <div data-testid="timeline-scroll" style={{ maxHeight: 560, overflowY: 'auto', paddingTop: 6, paddingRight: 4 }}>
      <Timeline
        items={nodes.map((n) => {
          const active = n.date === selected
          return {
            color: active ? token.colorPrimary : 'gray',
            content: (
              <a
                {...clickable(() => onSelect(n.date))}
                aria-label={`${n.date}, ${n.n}${n.predicted ? `, ${t('forecast.timeline.predicted')}` : ''}`}
                style={{
                  display: 'inline-flex',
                  alignItems: 'baseline',
                  gap: 6,
                  fontWeight: active ? 600 : 400,
                  color: active ? token.colorPrimary : token.colorText,
                }}
              >
                <span>{n.date}</span>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {n.n}
                </Typography.Text>
                {n.predicted && <span aria-hidden="true" style={{ color: token.colorPrimary }}>●</span>}
              </a>
            ),
          }
        })}
      />
    </div>
  )
}
