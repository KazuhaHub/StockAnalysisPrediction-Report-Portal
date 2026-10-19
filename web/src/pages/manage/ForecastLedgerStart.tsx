import { useEffect, useState } from 'react'
import { App, Alert, Button, Descriptions, Input, Modal, Space, Table, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import utc from 'dayjs/plugin/utc'
import timezone from 'dayjs/plugin/timezone'
import { Link } from 'react-router'
import { api, errText } from '../../api/client'
import type { ForecastPopulation, ForecastPopulationCandidate } from '../../api/types'

dayjs.extend(utc)
dayjs.extend(timezone)

// The ledger start (P5, decisions V3).
//
// Sets made before the boundary leave the record without a row being touched: they are still settled,
// counted and purged, they simply never become a member. The value is WRITE-ONCE and survives a
// restore, so the confirmation requires the admin to type the instant again — the same "type it to
// confirm" gate the account-delete flow uses, because an instant that moves the record is worth the
// two seconds.
//
// The instant is entered and shown in Asia/Shanghai (the exchange's civil zone, which is what every
// ledger date is expressed in) with its UTC rendering beside it, because the stored value is UTC and
// the admin has to be able to check the two agree.
const SHANGHAI = 'Asia/Shanghai'

function toShanghaiInput(iso: string): string {
  return dayjs(iso).tz(SHANGHAI).format('YYYY-MM-DDTHH:mm:ss')
}

export default function ForecastLedgerStart({
  open,
  value,
  onClose,
  onSaved,
}: {
  open: boolean
  value: ForecastPopulation
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [draft, setDraft] = useState('')
  const [confirming, setConfirming] = useState(false)
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    if (open) {
      setDraft(value.from ? toShanghaiInput(value.from) : '')
      setConfirming(false)
      setTyped('')
      setErr('')
    }
  }, [open, value.from])

  const valid = draft !== '' && dayjs.tz(draft, SHANGHAI).isValid()
  const utc = valid ? dayjs.tz(draft, SHANGHAI).utc().format('YYYY-MM-DDTHH:mm:ss[Z]') : ''

  const save = async () => {
    if (!valid) {
      setErr(t('forecast.population.bad_instant'))
      return
    }
    setBusy(true)
    setErr('')
    try {
      await api.post('/api/admin/forecast', { population_from: utc })
      message.success(t('forecast.population.saved'))
      onSaved()
      onClose()
    } catch (e) {
      // The server owns the bounds; its refusal is shown with its own code.
      setErr(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  const candidateColumns = [
    { title: t('forecast.population.col_id'), dataIndex: 'id', key: 'id', width: 80 },
    { title: t('forecast.population.col_symbol'), dataIndex: 'symbol', key: 'symbol' },
    { title: t('forecast.population.col_made_at'), dataIndex: 'made_at', key: 'made_at', render: (v: string) => dayjs(v).tz(SHANGHAI).format('YYYY-MM-DD HH:mm') },
    { title: t('forecast.population.col_state'), dataIndex: 'state', key: 'state', render: (v: string) => <Tag>{v}</Tag> },
    {
      title: t('forecast.population.col_orphan'),
      dataIndex: 'orphan',
      key: 'orphan',
      render: (v: boolean) => (v ? <Tag color="warning">{t('forecast.population.orphan')}</Tag> : null),
    },
  ]

  return (
    <Modal
      title={t('forecast.population.title')}
      open={open}
      onCancel={onClose}
      footer={null}
      width={720}
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          {t('forecast.population.help')}
        </Typography.Paragraph>

        {value.set ? (
          <>
            {value.damaged ? (
              <Alert type="error" showIcon title={t('forecast.population.damaged')} />
            ) : (
              <Descriptions column={1} size="small" bordered>
                <Descriptions.Item label={t('forecast.population.from')}>
                  {dayjs(value.from).tz(SHANGHAI).format('YYYY-MM-DD HH:mm:ss')}
                  <Typography.Text type="secondary" style={{ marginInlineStart: 8 }}>
                    ({value.from})
                  </Typography.Text>
                </Descriptions.Item>
                {(value.set_by || value.set_at) && (
                  <Descriptions.Item label={t('forecast.population.set_by')}>
                    {value.set_by || '—'}
                    {value.set_at && (
                      <Typography.Text type="secondary" style={{ marginInlineStart: 8 }}>
                        {dayjs(value.set_at).tz(SHANGHAI).format('YYYY-MM-DD HH:mm')}
                      </Typography.Text>
                    )}
                  </Descriptions.Item>
                )}
              </Descriptions>
            )}
            <Typography.Text type="secondary">
              {t('forecast.population.immutable')}{' '}
              <Link to="/manage/audit?action=forecast.population">{t('forecast.population.audit_link')}</Link>
            </Typography.Text>
          </>
        ) : (
          <>
            <Alert type="info" showIcon title={t('forecast.population.unset')} />
            <div>
              <Typography.Text>{t('forecast.population.instant')}</Typography.Text>
              <Input
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                placeholder="2026-10-19T00:00:00"
                aria-label={t('forecast.population.instant')}
                style={{ marginTop: 4, maxWidth: 320 }}
              />
              <Typography.Text type="secondary" style={{ display: 'block', marginTop: 4 }}>
                {t('forecast.population.tz_note')}
                {utc && <> → {utc}</>}
              </Typography.Text>
            </div>
            {value.bound && (
              <Typography.Text type="secondary">
                {t('forecast.population.bound')}: {dayjs(value.bound).tz(SHANGHAI).format('YYYY-MM-DD HH:mm')} ({value.bound})
              </Typography.Text>
            )}

            <Typography.Text>{t('forecast.population.candidates')}</Typography.Text>
            <Table<ForecastPopulationCandidate>
              rowKey="id"
              size="small"
              pagination={false}
              columns={candidateColumns}
              dataSource={value.candidates}
              locale={{ emptyText: t('forecast.population.no_candidates') }}
            />

            {err && <Alert type="error" showIcon title={err} />}

            {!confirming ? (
              <Button type="primary" danger disabled={!valid} onClick={() => setConfirming(true)}>
                {t('forecast.population.save')}
              </Button>
            ) : (
              <>
                <Alert
                  type="warning"
                  showIcon
                  title={t('forecast.population.confirm_title')}
                  description={t('forecast.population.confirm_body')}
                />
                <Typography.Text>{t('forecast.population.type_again')}</Typography.Text>
                <Input
                  value={typed}
                  onChange={(e) => setTyped(e.target.value)}
                  placeholder={draft}
                  aria-label={t('forecast.population.type_again')}
                />
                <Space>
                  <Button onClick={() => setConfirming(false)}>{t('common.cancel')}</Button>
                  <Button
                    type="primary"
                    danger
                    loading={busy}
                    disabled={typed !== draft}
                    onClick={save}
                  >
                    {t('forecast.population.confirm_save')}
                  </Button>
                </Space>
              </>
            )}
          </>
        )}

        {!value.set && !value.bound && (
          <Typography.Text type="secondary">{t('forecast.population.empty_ledger')}</Typography.Text>
        )}
      </Space>
    </Modal>
  )
}
