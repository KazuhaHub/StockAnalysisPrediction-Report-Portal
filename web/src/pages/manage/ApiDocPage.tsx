import { useEffect, useState, type CSSProperties } from 'react'
import { Button, Card, Empty, Input, Pagination, Space, Spin, Table, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import Markdown from '../../components/Markdown'
import { usePrefs } from '../../prefs'
import { specToEndpoints, type ApiEndpoint, type ApiParam, type ApiError } from './openapiDoc'

const METHOD_COLORS: Record<string, string> = {
  GET: 'green',
  POST: 'blue',
  PUT: 'gold',
  PATCH: 'orange',
  DELETE: 'red',
}

const CODE_BLOCK: CSSProperties = {
  background: 'rgba(128,128,128,0.12)',
  padding: 12,
  borderRadius: 8,
  overflow: 'auto',
  fontSize: 12,
  lineHeight: 1.6,
  margin: '4px 0 0',
  whiteSpace: 'pre',
}

function EndpointCard({ e }: { e: ApiEndpoint }) {
  const { t } = useTranslation()
  return (
    <Card size="small">
      <Space orientation="vertical" size={10} style={{ width: '100%' }}>
        <Space wrap align="center">
          <Tag color={METHOD_COLORS[e.method] || 'default'} style={{ fontFamily: 'monospace', fontWeight: 600, margin: 0 }}>
            {e.method}
          </Tag>
          <Typography.Text code copyable={{ text: e.path }} style={{ fontSize: 14 }}>
            {e.path}
          </Typography.Text>
          <Tag>{t(`scope.${e.scope}`, { defaultValue: e.scope })}</Tag>
        </Space>
        <Typography.Text type="secondary">{e.summary}</Typography.Text>

        {e.params.length > 0 && (
          <Table<ApiParam>
            size="small"
            pagination={false}
            scroll={{ x: 'max-content' }}
            rowKey={(p) => `${p.in}:${p.name}`}
            dataSource={e.params}
            columns={[
              { title: t('apidoc.param'), dataIndex: 'name', width: 130, render: (n: string) => <Typography.Text code>{n}</Typography.Text> },
              { title: t('apidoc.in'), dataIndex: 'in', width: 64 },
              { title: t('apidoc.type'), dataIndex: 'type', width: 108 },
              {
                title: t('apidoc.required'),
                dataIndex: 'required',
                width: 60,
                align: 'center',
                render: (v: boolean) => (v ? <Tag color="red">{t('apidoc.required')}</Tag> : <Typography.Text type="secondary">—</Typography.Text>),
              },
              { title: t('apidoc.desc'), dataIndex: 'desc' },
            ]}
          />
        )}

        <div>
          <Typography.Text strong style={{ fontSize: 12 }}>
            {t('apidoc.requestExample')}
          </Typography.Text>
          <pre style={CODE_BLOCK}>{e.requestExample}</pre>
        </div>
        <div>
          <Typography.Text strong style={{ fontSize: 12 }}>
            {t('apidoc.responseExample')}
          </Typography.Text>
          <pre style={CODE_BLOCK}>{e.responseExample}</pre>
        </div>

        {e.errors.length > 0 && (
          <Table<ApiError>
            size="small"
            pagination={false}
            scroll={{ x: 'max-content' }}
            rowKey={(er) => String(er.code)}
            dataSource={e.errors}
            columns={[
              { title: t('apidoc.statusCode'), dataIndex: 'code', width: 80, render: (c: number) => <Tag color="volcano">{c}</Tag> },
              { title: t('apidoc.when'), dataIndex: 'when' },
            ]}
          />
        )}

        {e.notes && (
          <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
            {e.notes}
          </Typography.Paragraph>
        )}
      </Space>
    </Card>
  )
}

// Live reference for the /api/v1 machine API, rendered from the served openapi.json.
export default function ApiDocPage() {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const [doc, setDoc] = useState<{ conventions: string; endpoints: ApiEndpoint[] } | null>(null)
  const [failed, setFailed] = useState(false)
  const [retry, setRetry] = useState(0)
  const [query, setQuery] = useState('')
  const [page, setPage] = useState(1)
  const apiDocURL = `/api/openapi.json?lang=${encodeURIComponent(lang)}`

  useEffect(() => {
    let ignore = false
    setDoc(null)
    setFailed(false)
    fetch(apiDocURL, { credentials: 'same-origin' })
      .then((r) => { if (!r.ok) throw new Error(String(r.status)); return r.json() })
      .then((spec) => {
        if (!ignore) setDoc(specToEndpoints(spec, window.location.origin))
      })
      .catch(() => {
        if (!ignore) setFailed(true)
      })
    return () => {
      ignore = true
    }
  }, [apiDocURL, retry])

  if (failed) return <Space orientation="vertical"><Typography.Text type="danger">{t('apidoc.loadFailed')}</Typography.Text><Button onClick={() => setRetry((v) => v + 1)}>{t('common.retry')}</Button></Space>
  if (!doc) return <Spin />
  const terms = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean)
  const endpoints = doc.endpoints.filter((e) => terms.every((term) => `${e.method} ${e.path} ${e.summary} ${e.scope}`.toLocaleLowerCase().includes(term)))
  const currentPage = Math.min(page, Math.max(1, Math.ceil(endpoints.length / 10)))

  return (
    <Space orientation="vertical" size={12} style={{ width: '100%' }}>
      <Space wrap>
        <Tag color="geekblue">OpenAPI 3.1</Tag>
        <Button size="small" href={apiDocURL} target="_blank" rel="noreferrer">
          {t('apidoc.download')}
        </Button>
      </Space>
      <Card size="small" title={t('apidoc.conventions')}>
        <Markdown md={doc.conventions} />
      </Card>
      <Input.Search allowClear aria-label={t('manage.apiSearch')} placeholder={t('manage.apiSearch')} value={query} onChange={(e) => { setQuery(e.target.value); setPage(1) }} />
      {endpoints.slice((currentPage - 1) * 10, currentPage * 10).map((e) => (
        <EndpointCard key={`${e.method} ${e.path}`} e={e} />
      ))}
      {endpoints.length === 0 && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('manage.noResults')} />}
      <Pagination current={currentPage} pageSize={10} total={endpoints.length} onChange={setPage} showSizeChanger={false} hideOnSinglePage />
    </Space>
  )
}
