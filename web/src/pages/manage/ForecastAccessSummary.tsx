import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, Table, Tag, Typography } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { ForecastAccessOverview, ForecastOUAccess, ForecastScope, UserGroupRow } from '../../api/types'

// Who may read the forecast record, per OU, as the SERVER resolved it (GET /api/admin/forecast/access).
//
// Nothing here recomputes inheritance: the table shows the server's effective scope and the OU that
// decided it, so it cannot disagree with what a member is actually allowed to open. Names come from
// the OU list, because the access overview identifies units by id only. The values are edited in the
// OU editor; this card is the read-only picture, with a link to where it is changed.

type State =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; overview: ForecastAccessOverview; names: Map<number, string> }

const TONE: Record<ForecastScope, string> = { all: 'green', own: 'blue', none: 'default' }

export default function ForecastAccessSummary() {
  const { t } = useTranslation()
  const [state, setState] = useState<State>({ kind: 'loading' })
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([
      api.get<ForecastAccessOverview>('/api/admin/forecast/access'),
      api.get<{ groups: UserGroupRow[] }>('/api/admin/groups'),
    ])
      .then(([overview, list]) => {
        if (!cancelled) setState({ kind: 'ready', overview, names: new Map((list.groups ?? []).map((g) => [g.id, g.name])) })
      })
      .catch((e) => {
        if (!cancelled) setState({ kind: 'error', message: errText(e, t) })
      })
    return () => {
      cancelled = true
    }
    // `t` is stable in the app; under a test double it is not, and depending on it would refetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revision])

  const rows = useMemo(() => (state.kind === 'ready' ? state.overview.groups : []), [state])
  const nameOf = (id: number) => (state.kind === 'ready' ? state.names.get(id) : undefined) ?? `#${id}`

  const body = () => {
    switch (state.kind) {
      case 'loading':
        return <Typography.Text type="secondary">{t('common.loading')}</Typography.Text>
      case 'error':
        return (
          <Alert
            type="error"
            showIcon
            title={state.message}
            action={<Button size="small" onClick={() => setRevision((n) => n + 1)}>{t('common.retry')}</Button>}
          />
        )
      case 'ready': {
        const { counts } = state.overview
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <Typography.Text>{t('forecast.access.summary', { all: counts.all, own: counts.own, none: counts.none })}</Typography.Text>
            <Table<ForecastOUAccess>
              size="small"
              rowKey="id"
              dataSource={rows}
              pagination={{ pageSize: 10, hideOnSinglePage: true }}
              scroll={{ x: 'max-content' }}
              columns={[
                {
                  title: t('forecast.access.col_ou'),
                  key: 'ou',
                  render: (_, r) => <Link to={`/manage/users?tab=groups&ou=${r.id}`}>{nameOf(r.id)}</Link>,
                },
                {
                  title: t('forecast.access.col_own'),
                  key: 'own',
                  render: (_, r) =>
                    r.forecast_scope ? <Tag>{t(`forecast.scope.${r.forecast_scope}`)}</Tag> : <Typography.Text type="secondary">{t('forecast.access.inherited')}</Typography.Text>,
                },
                {
                  title: t('forecast.access.col_effective'),
                  key: 'effective',
                  render: (_, r) => (
                    <>
                      <Tag color={TONE[r.forecast_scope_effective]}>{t(`forecast.scope.${r.forecast_scope_effective}`)}</Tag>
                      {r.external && <Tag color="volcano">{t('forecast.access.external')}</Tag>}
                    </>
                  ),
                },
                {
                  title: t('forecast.access.col_source'),
                  key: 'source',
                  render: (_, r) => (r.forecast_scope_source ? nameOf(r.forecast_scope_source) : '—'),
                },
                {
                  title: t('forecast.access.col_accounts'),
                  key: 'accounts',
                  render: (_, r) => `${r.counts.all} / ${r.counts.own} / ${r.counts.none}`,
                },
              ]}
            />
          </div>
        )
      }
    }
  }

  return (
    <Card
      title={t('forecast.access.title')}
      extra={<Link to="/manage/users?tab=groups">{t('forecast.access.edit')}</Link>}
    >
      <Typography.Paragraph type="secondary">{t('forecast.access.help')}</Typography.Paragraph>
      {body()}
    </Card>
  )
}
