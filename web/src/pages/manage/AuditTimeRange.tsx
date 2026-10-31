import { useState } from 'react'
import { Button, DatePicker, Typography } from 'antd'
import { ClockCircleOutlined, DownOutlined } from '@ant-design/icons'
import dayjs, { type Dayjs } from 'dayjs'
import { useTranslation } from 'react-i18next'

import PopoverOrSheet from '../../components/PopoverOrSheet'

import { AUDIT_PRESETS, instantToWall, wallToInstant, type AuditRange } from '../../lib/auditQuery'

interface Props {
  value: AuditRange
  onChange: (next: AuditRange) => void
  /** The panel time zone; '' follows the reader's. An absolute range is picked in it, as the table shows. */
  tz: string
}

const WALL = 'YYYY-MM-DD HH:mm:ss'
const SHORT = 'MM-DD HH:mm'
const LONG = 'YYYY-MM-DD HH:mm'

// The time range, as Cloudflare's logs pick one: relative presets on the left, each ending at the
// moment the filters were set, and an absolute range on the right, picked as wall-clock times in
// the panel zone and sent as instants.
export default function AuditTimeRange({ value, onChange, tz }: Props) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<[Dayjs, Dayjs] | null>(null)

  // The picker works in the browser's zone; a panel wall clock is handed to it as a plain local time.
  const toPicker = (instant: string) => dayjs(instantToWall(instant, tz).replace(' ', 'T'))
  const fromPicker = (d: Dayjs) => wallToInstant(d.format(WALL), tz)

  // An absolute range names its year unless both ends fall in the current one.
  const absolute = (from: Dayjs, to: Dayjs) => {
    const fmt = from.year() === dayjs().year() && to.year() === dayjs().year() ? SHORT : LONG
    return t('audit.range.between', { from: from.format(fmt), to: to.format(fmt) })
  }
  const label = 'preset' in value ? t(`audit.range.${value.preset}`) : absolute(toPicker(value.from), toPicker(value.to))

  const show = (next: boolean) => {
    if (next) setDraft('from' in value ? [toPicker(value.from), toPicker(value.to)] : null)
    setOpen(next)
  }

  const panel = (
    <div className="rp-audit-range">
      {/* A plain list of buttons: each applies at once, so it is not a listbox to navigate. Focus
          starts on the current preset, so the panel is reachable from the keyboard. */}
      <ul className="rp-audit-range__presets" aria-label={t('audit.range.presets')}>
        {AUDIT_PRESETS.map((p) => {
          const selected = 'preset' in value && value.preset === p
          return (
            <li key={p}>
              <button
                type="button"
                aria-pressed={selected}
                autoFocus={selected}
                className={selected ? 'is-selected' : undefined}
                onClick={() => {
                  onChange({ preset: p })
                  setOpen(false)
                }}
              >
                {t(`audit.range.${p}`)}
              </button>
            </li>
          )
        })}
      </ul>
      <div className="rp-audit-range__absolute">
        <Typography.Text strong>{t('audit.range.absolute')}</Typography.Text>
        <DatePicker.RangePicker
          showTime={{ format: 'HH:mm' }}
          format="YYYY-MM-DD HH:mm"
          value={draft}
          onChange={(v) => setDraft(v && v[0] && v[1] ? [v[0], v[1]] : null)}
          placeholder={[t('audit.range.start'), t('audit.range.end')]}
          // The two inputs are named by their placeholders; the group by this label.
          aria-label={t('audit.range.absolute')}
          classNames={{ popup: { root: 'rp-picker-popup' } }}
        />
        {tz && (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {t('audit.range.zone', { tz })}
          </Typography.Text>
        )}
        <Button
          type="primary"
          size="small"
          disabled={!draft}
          onClick={() => {
            if (!draft) return
            // To the end of the chosen minute, so "until 18:00" keeps what happened during 18:00.
            onChange({ from: fromPicker(draft[0].second(0)), to: fromPicker(draft[1].second(59)) })
            setOpen(false)
          }}
        >
          {t('audit.apply')}
        </Button>
      </div>
    </div>
  )

  return (
    <PopoverOrSheet open={open} onOpenChange={show} title={t('audit.range.label')} content={panel}>
      <Button icon={<ClockCircleOutlined />} aria-haspopup="dialog" aria-expanded={open} aria-label={t('audit.range.aria', { range: label })}>
        {label}
        <DownOutlined style={{ fontSize: 10 }} />
      </Button>
    </PopoverOrSheet>
  )
}
