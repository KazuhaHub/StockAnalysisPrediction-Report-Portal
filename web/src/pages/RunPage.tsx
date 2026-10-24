import { useEffect, useState, type CSSProperties } from 'react'
import { Alert, Button, Card, Empty, Result, Segmented, Space, Spin, Tag, Typography } from 'antd'
import { ArrowLeftOutlined, DownloadOutlined } from '@ant-design/icons'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, qs, ApiError, errText } from '../api/client'
import type { ReportPredictions, RunResp, SubTab } from '../api/types'
import Markdown from '../components/Markdown'
import EditReportButton from '../components/EditReportButton'
import ReaderControls from '../components/ReaderControls'
import VersionSwitcher from '../components/VersionSwitcher'
import { ExportPdfButton } from '../components/ExportButtons'
import { useReaderPrefs } from '../reader'
import { NO_ITEM_TOOLTIP } from '../lib/segmented'
import ForecastReadingCard from '../components/ForecastReadingCard'
import ForecastSummaryChip from '../components/ForecastSummaryChip'
import { useAuth } from '../auth'

export default function RunPage() {
  const { t } = useTranslation()
  const { user, admin, forecast, forecastReader } = useAuth()
  const forecastEnabled = forecast === undefined || forecast.scope !== 'none'
  const forecastCardEnabled = forecastEnabled && (admin || forecastReader === undefined || forecastReader === 'card' || forecastReader === 'full')
  const forecastFullEnabled = forecastEnabled && (admin || forecastReader === 'full')
  const accessKey = `${user ?? ''}\0${admin}\0${forecast?.scope ?? 'legacy'}\0${forecast?.external ?? false}\0${forecastReader ?? 'legacy'}`
  const { key = '' } = useParams()
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
  const { fontSize, fontWeight, wide } = useReaderPrefs()
  const readerVars = { '--md-fs': `${fontSize}px`, '--md-fw': String(fontWeight) } as CSSProperties
  // Same optimal reading width as the stock page: fill up to the cap, then center.
  const docMax = wide ? 1440 : 1080
  const [data, setData] = useState<RunResp | null>(null)
  const [dataAccess, setDataAccess] = useState('')
  const [loading, setLoading] = useState(true)
  const [notFound, setNotFound] = useState(false)
  const [prediction, setPrediction] = useState<{ reportId?: number; accessKey?: string; data?: ReportPredictions; unavailable?: boolean; error: string }>({ error: '' })
  const [predictionNonce, setPredictionNonce] = useState(0)

  const r = sp.get('r') || ''

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setNotFound(false)
    api
      .get<RunResp>(`/api/run/${encodeURIComponent(key)}${qs({ r })}`)
      .then((next) => { if (!cancelled) { setData(next); setDataAccess(accessKey) } })
      .catch((e) => {
        if (cancelled) return
        if (e instanceof ApiError && e.status === 404) setNotFound(true)
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [key, r, accessKey])

  const reportId = data?.rep?.id
  useEffect(() => {
    setPrediction({ reportId, accessKey, error: '' })
    if (!reportId || !forecastCardEnabled) return
    let cancelled = false
    api.get<ReportPredictions>(`/api/report/${reportId}/predictions?include=payload`)
      .then((next) => { if (!cancelled) setPrediction({ reportId, accessKey, data: next, error: '' }) })
      .catch((error) => {
        if (cancelled) return
        const unavailable = error instanceof ApiError && error.status === 403 && error.code === 'forecast_unavailable'
        setPrediction({ reportId, accessKey, unavailable, error: unavailable ? '' : errText(error, t) })
      })
    return () => { cancelled = true }
    // `t` is intentionally not a dependency: language changes remount the translated shell, while
    // this request is keyed only by the report, access stage and an explicit retry.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reportId, forecastCardEnabled, predictionNonce, accessKey])

  if (notFound) {
    return (
      <Result
        status="404"
        title={t('home.empty')}
        extra={
          <Button type="primary" onClick={() => navigate('/')}>
            {t('stock.back')}
          </Button>
        }
      />
    )
  }
  if (loading && !data) {
    return (
      <div style={{ padding: 80, textAlign: 'center' }}>
        <Spin size="large" />
      </div>
    )
  }
  if (!data) return null
  const rep = data.rep
  const currentPrediction = prediction.reportId === rep?.id && prediction.accessKey === accessKey ? prediction : { reportId: undefined, data: undefined, error: '', unavailable: false }
  const selectedEdition = data.tabs.find((tab) => tab.id === data.selId)?.version
  // One entry per REPORT, showing the written form currently selected for it. The version axis is
  // the switcher's job; collapsing here is what stops two forms of one analysis from looking like
  // two identical tabs.
  //
  // Keyed by type AND title, because that is what "one report" means: two forms of one analysis
  // share every identity component but version (ADR 0024), while two genuinely different reports of
  // one type differ by title — one code+date+subtype legitimately carries several. Collapsing on
  // the type alone swallowed the second kind too, which left the survivor carrying the number
  // orderAndDefault gave it ("Trade 2") with no sibling on screen for the number to refer to, and
  // the other report unreachable from the strip.
  const typeTabs = Array.from(
    data.tabs
      .reduce((byReport, tab) => {
        // Keep the SELECTED form for its report, so the strip's value matches what is being read;
        // otherwise the first one seen. Map preserves insertion order, so the strip keeps the
        // server's ordering.
        const key = `${tab.rtype}\u0000${tab.title}`
        if (!byReport.has(key) || tab.id === data.selId) byReport.set(key, tab)
        return byReport
      }, new Map<string, SubTab>())
      .values(),
  )

  return (
    <Spin spinning={loading}>
      {/* No timeline → just the centered reading column (same width/centering as the stock
          page's, so the two readers match). */}
      <div className={`rp-reader${wide ? ' rp-reader--wide' : ''}`} style={{ '--rp-doc-max': `${docMax}px` } as CSSProperties}>
        <div className="rp-reader__doc">
          <Space orientation="vertical" size={16} style={{ width: '100%' }}>
        <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
          <Space size={12} wrap>
            <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/')}>
              {t('stock.back')}
            </Button>
            <Typography.Title level={4} style={{ margin: 0 }}>
              {data.name || data.symbol}{' '}
              <Typography.Text type="secondary" style={{ fontSize: 15 }}>
                {data.date}
              </Typography.Text>
            </Typography.Title>
            {rep && rep.name && data.name && rep.name !== data.name && (
              <Tag color="orange">
                {t('stock.asOf')}: {rep.name}
              </Tag>
            )}
          </Space>
          {rep && (
            <Space wrap>
              <Button icon={<DownloadOutlined />} href={`/report/${rep.id}/md`}>
                {t('stock.exportMd')}
              </Button>
              <ExportPdfButton
                id={rep.id}
                report={{ title: rep.displayTitle, date: rep.date, source: rep.source, html: rep.html, md: rep.md }}
              />
              <EditReportButton reportId={rep.id} />
            </Space>
          )}
        </Space>

        {/* Two axes, two controls (ADR 0024). The strip is one entry per report TYPE; the
            switcher below picks which written form of it to read. Without the collapse, two forms
            of one analysis appear as two tabs with the same label, which reads as a duplicate
            rather than as a choice. */}
        {typeTabs.length > 1 && (
          // Report-type strip: a horizontal-scroll Segmented so it swipes smoothly on
          // mobile instead of dragging the whole page.
          <div style={{ overflowX: 'auto', overscrollBehaviorX: 'contain' }}>
            <Segmented
              value={data.selId}
              onChange={(v) => setSp({ r: String(v) })}
              // As on the stock page: the tab names the type and generator version; the tooltip
              // names the report.
              options={typeTabs.map((s) => ({
                label: s.label,
                value: s.id,
                title: s.title && s.title !== s.label ? s.title : NO_ITEM_TOOLTIP,
              }))}
            />
          </div>
        )}
          {rep && <VersionSwitcher reportId={rep.id} onPick={(id) => setSp({ r: String(id) })} />}
          <Card className="rp-doc-card" title={rep?.displayTitle} extra={rep ? <ReaderControls /> : undefined} style={readerVars}>
            {rep && forecastFullEnabled && !currentPrediction.unavailable && <ForecastSummaryChip prediction={dataAccess === accessKey ? data.prediction : undefined} loading={dataAccess !== accessKey && loading} />}
            {rep ? <Markdown md={rep.md} html={rep.html} /> : <Empty />}
          </Card>
          {rep && forecastCardEnabled && currentPrediction.error && (
            <Alert
              type="error"
              showIcon
              title={currentPrediction.error}
              action={<Button size="small" onClick={() => setPredictionNonce((n) => n + 1)}>{t('common.retry')}</Button>}
            />
          )}
          {rep && forecastCardEnabled && !currentPrediction.error && !currentPrediction.unavailable && (
            <ForecastReadingCard
              sets={currentPrediction.data?.sets ?? []}
              countedSetID={currentPrediction.data?.counted_set_id}
              displaySetID={currentPrediction.data?.display_set_id}
              settlementPaused={currentPrediction.data?.settlement_paused}
              covered={currentPrediction.data?.covered}
              otherSets={[]}
              symbol={data.symbol}
              reportDate={rep.date}
              receivedAt={rep.time}
              edition={selectedEdition}
              sibling={currentPrediction.data?.sibling}
              countedEdition={currentPrediction.data?.counted_edition}
              loading={!currentPrediction.data && !currentPrediction.error}
              allowPayload={!forecast?.external}
              allowMembership={admin || forecast?.scope === 'all' && !forecast.external}
              allowOrphans={!forecast?.external}
              reportKind={rep.kind}
            />
          )}
        </Space>
      </div>
    </div>
  </Spin>
  )
}
