import { useMemo, useState } from 'react'
import { Button, DatePicker, Dropdown, Form, Modal, Segmented, Select, Typography } from 'antd'
import { DownOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { Dayjs } from 'dayjs'
import { readerZone, renderAnnouncementVars } from '../lib/announcementVars'
import { tzOptions } from '../lib/timezones'

type Kind = 'time' | 'countdown'
type Format = 'datetime' | 'date' | 'time'

/**
 * The editor's "insert variable" menu. It writes the same template text an operator could type, so
 * there is one syntax and the menu is a shortcut to it, never a second representation. A moment is
 * picked in the operator's own clock and stored as a UTC instant, which is what makes it render
 * correctly in every reader's zone.
 */
export default function AnnouncementVarInsert({ onInsert, site }: { onInsert: (text: string) => void; site: string }) {
  const { t, i18n } = useTranslation()
  const [kind, setKind] = useState<Kind | null>(null)
  const [at, setAt] = useState<Dayjs | null>(null)
  const [format, setFormat] = useState<Format>('datetime')
  const [zone, setZone] = useState('')
  const zones = useMemo(() => tzOptions(t('announcementVars.readerZone')), [t])

  const token = (() => {
    if (!kind || !at) return ''
    const instant = at.toDate().toISOString().slice(0, 16) + 'Z'
    if (kind === 'countdown') return `{{countdown:${instant}}}`
    const opts = [format !== 'datetime' ? format : '', zone].filter(Boolean)
    return `{{time:${instant}${opts.map((o) => `|${o}`).join('')}}}`
  })()

  const open = (k: Kind) => {
    setKind(k)
    setAt(null)
    setFormat('datetime')
    setZone('')
  }
  const close = () => setKind(null)

  return (
    <>
      <Dropdown
        trigger={['click']}
        menu={{
          items: [
            { key: 'now', label: t('announcementVars.now') },
            { key: 'time', label: t('announcementVars.time') },
            { key: 'countdown', label: t('announcementVars.countdown') },
            { key: 'site', label: t('announcementVars.site') },
          ],
          onClick: ({ key }) => {
            if (key === 'now') onInsert('{{now}}')
            else if (key === 'site') onInsert('{{site}}')
            else open(key as Kind)
          },
        }}
      >
        <Button size="small" type="link" style={{ paddingInline: 0 }}>
          {t('announcementVars.insert')} <DownOutlined />
        </Button>
      </Dropdown>
      <Modal
        open={kind !== null}
        title={kind === 'countdown' ? t('announcementVars.countdown') : t('announcementVars.time')}
        okText={t('announcementVars.insertOk')}
        okButtonProps={{ disabled: !token }}
        onOk={() => {
          if (!token) return
          onInsert(token)
          close()
        }}
        onCancel={close}
        destroyOnHidden
      >
        <Form layout="vertical">
          <Form.Item label={t('announcementVars.moment')} extra={t('announcementVars.momentHint', { zone: readerZone() })}>
            <DatePicker showTime={{ format: 'HH:mm' }} format="YYYY-MM-DD HH:mm" value={at} onChange={setAt} style={{ width: '100%' }} />
          </Form.Item>
          {kind === 'time' && (
            <>
              <Form.Item label={t('announcementVars.format')}>
                <Segmented<Format>
                  value={format}
                  onChange={setFormat}
                  options={[
                    { value: 'datetime', label: t('announcementVars.format.datetime') },
                    { value: 'date', label: t('announcementVars.format.date') },
                    { value: 'time', label: t('announcementVars.format.time') },
                  ]}
                />
              </Form.Item>
              <Form.Item label={t('announcementVars.zone')} extra={t('announcementVars.zoneHint')}>
                <Select showSearch value={zone} onChange={setZone} options={zones} />
              </Form.Item>
            </>
          )}
          {token && (
            <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
              {t('announcementVars.sample')}{' '}
              <Typography.Text>
                {renderAnnouncementVars(token, { now: new Date(), zone: readerZone(), site, lang: i18n?.language || 'zh-CN' })}
              </Typography.Text>
              <br />
              <Typography.Text code copyable>
                {token}
              </Typography.Text>
            </Typography.Paragraph>
          )}
        </Form>
      </Modal>
    </>
  )
}
