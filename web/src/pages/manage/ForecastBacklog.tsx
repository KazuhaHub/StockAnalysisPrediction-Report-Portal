import { Alert, Card, Descriptions, Table, Typography } from 'antd'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import type { ForecastSettings } from '../../api/types'

export default function ForecastBacklog({ backlog }: { backlog: ForecastSettings['backlog'] }) {
  const { t } = useTranslation()
  if (!backlog) return <Alert type="warning" showIcon title={t('forecast.backlog.unavailable')} />
  // Nothing due is the ordinary state; a grid of zeros reads as a fault to look into.
  if (backlog.due === 0) return <Card size="small" title={t('forecast.backlog.title')}>
    <Typography.Text type="secondary">{t('forecast.backlog.none', { at: dayjs(backlog.as_of).format('YYYY-MM-DD HH:mm') })}</Typography.Text>
  </Card>
  return <Card size="small" title={t('forecast.backlog.title')}>
    {backlog.overdue > 0 && <Alert type="warning" showIcon title={t('forecast.backlog.warning', { n: backlog.overdue })} style={{ marginBottom: 12 }} />}
    <Descriptions size="small" column={{ xs: 1, sm: 3 }} items={[
      { key: 'due', label: t('forecast.backlog.due'), children: backlog.due },
      { key: 'retrying', label: t('forecast.backlog.retrying'), children: backlog.retrying },
      { key: 'oldest', label: t('forecast.backlog.oldest'), children: t('forecast.backlog.minutes', { n: backlog.oldest_late_minutes ?? 0 }) },
    ]} />
    <Typography.Paragraph type="secondary">{t('forecast.backlog.hint')} {t('forecast.backlog.asof', { at: dayjs(backlog.as_of).format('YYYY-MM-DD HH:mm:ss') })}</Typography.Paragraph>
    {backlog.items.length > 0 && <Table size="small" pagination={false} scroll={{ x: 650 }} rowKey={(r) => `${r.set_id}-${r.checkpoint}`} dataSource={backlog.items} columns={[
      { title: t('forecast.backlog.record'), render: (_, r) => `${r.symbol} · #${r.set_id} · ${t(`forecast.cp.${r.checkpoint}`)}` },
      { title: t('forecast.backlog.session'), dataIndex: 'due' },
      { title: t('forecast.backlog.retry'), render: (_, r) => r.next_attempt_at ? dayjs(r.next_attempt_at).format('YYYY-MM-DD HH:mm:ss') : '—' },
      { title: t('forecast.backlog.failure'), render: (_, r) => <div>
        <div>{r.refusal ?? '—'}</div>
        {r.failure ? <>
          <Typography.Text>{t(`forecast.backlog.stage_${r.failure.stage}`, { defaultValue: r.failure.stage })} · {r.failure.source || '—'}</Typography.Text>
          <div>{r.failure.detail}</div>
          <Typography.Text type="secondary">{r.failure.from} → {r.failure.to} · {dayjs(r.failure.at).format('YYYY-MM-DD HH:mm:ss')}</Typography.Text>
        </> : <Typography.Text type="secondary">{t('forecast.backlog.no_detail')}</Typography.Text>}
      </div> },
    ]} />}
    {backlog.truncated && <Typography.Text type="secondary">{t('forecast.backlog.truncated')}</Typography.Text>}
  </Card>
}
