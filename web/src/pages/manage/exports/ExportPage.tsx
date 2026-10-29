import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { App, Alert, Button, Card, Collapse, Grid, Segmented, Space, Typography, theme } from 'antd'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../../api/client'
import type { ExportFilter, ExportJob, ExportPreviewResp, ExportSource, SettingsResp } from '../../../api/types'
import LoadGate from '../../../components/LoadGate'
import {
  applySelection,
  cleanFilter,
  emptyFilter,
  encodeFilter,
  isChipExpressible,
  loadColumns,
  parseExportParams,
  saveColumns,
  selectionOf,
  validateFilter,
} from '../../../lib/exportFilter'
import { notifyExportsChanged } from '../../../lib/exportJobs'
import { panelToday } from '../../../lib/exportTime'
import ColumnsDialog from './ColumnsDialog'
import ConditionBuilder from './ConditionBuilder'
import ExportDialog from './ExportDialog'
import ExportHistory from './ExportHistory'
import FilterBar from './FilterBar'
import ResultsPanel from './ResultsPanel'
import SourceRail from './SourceRail'
import TypeTree from './TypeTree'
import { useExportLabels } from './labels'

type Mode = 'filter' | 'advanced'
const DEBOUNCE_MS = 300

// Data export (ADR 0045): pick a source, narrow it with chips or the condition builder, watch the
// count and preview follow, then submit a background export task. The filter is URL state, so a
// view can be shared and other pages can link here with their own filter pre-filled.
export default function ExportPage() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const { notification } = App.useApp()
  const labels = useExportLabels()
  const compact = !Grid.useBreakpoint().lg
  const [params, setParams] = useSearchParams()
  const paramsRef = useRef(params)
  paramsRef.current = params

  const [sources, setSources] = useState<ExportSource[] | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [tz, setTz] = useState('')
  const [sourceKey, setSourceKey] = useState('')
  const [filter, setFilterState] = useState<ExportFilter>(emptyFilter())
  const [mode, setMode] = useState<Mode>('filter')
  const [modeHint, setModeHint] = useState(false)
  const [columns, setColumns] = useState<string[]>([])
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(50)
  const [preview, setPreview] = useState<ExportPreviewResp | null>(null)
  const [loading, setLoading] = useState(false)
  const [previewErr, setPreviewErr] = useState('')
  const [colsOpen, setColsOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)

  const source = sources?.find((s) => s.key === sourceKey)
  const clean = useMemo(() => cleanFilter(filter), [filter])
  const cleanKey = JSON.stringify(clean)
  const today = panelToday(tz)

  // --- loading -----------------------------------------------------------------------------
  const lastWritten = useRef('')
  const applyUrl = useCallback(
    (list: ExportSource[], p: URLSearchParams) => {
      const parsed = parseExportParams(p)
      const src = list.find((s) => s.key === parsed.source) ?? list[0]
      if (!src) return
      const f = parsed.filter ?? emptyFilter()
      setSourceKey(src.key)
      setFilterState(f)
      setMode(isChipExpressible(cleanFilter(f)) ? 'filter' : 'advanced')
      setModeHint(false)
      setColumns(loadColumns(src.key, src.columns))
      setPage(1)
    },
    [],
  )

  const load = useCallback(() => {
    setLoadErr('')
    api
      .get<{ sources: ExportSource[] }>('/api/admin/exports/sources')
      .then((r) => {
        setSources(r.sources)
        applyUrl(r.sources, paramsRef.current)
      })
      .catch((e) => setLoadErr(errText(e, t)))
    // The panel zone only decides how times read; a failure leaves them in the viewer's zone.
    api
      .get<SettingsResp>('/api/admin/settings')
      .then((s) => setTz(s?.timezone ?? ''))
      .catch(() => {})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [applyUrl, t])
  useEffect(load, [load])

  // Mirror source + filter into the URL (replace, so each edit is not a history entry).
  useEffect(() => {
    if (!sourceKey) return
    const next = new URLSearchParams({ source: sourceKey })
    if (clean.items.length > 0) next.set('f', encodeFilter(clean))
    const s = next.toString()
    lastWritten.current = s
    if (s !== params.toString()) setParams(next, { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sourceKey, cleanKey])

  // Back/forward (or a link to this page) changes the URL under us: follow it.
  useEffect(() => {
    if (sources && params.toString() !== lastWritten.current) applyUrl(sources, params)
  }, [params, sources, applyUrl])

  // --- preview -----------------------------------------------------------------------------
  const reqId = useRef(0)
  const lastFilterKey = useRef('')
  useEffect(() => {
    if (!source || columns.length === 0) return
    if (validateFilter(clean)) return
    const filterChanged = lastFilterKey.current !== `${source.key}|${cleanKey}`
    lastFilterKey.current = `${source.key}|${cleanKey}`
    const handle = window.setTimeout(
      async () => {
        const id = ++reqId.current
        setLoading(true)
        try {
          const r = await api.post<ExportPreviewResp>('/api/admin/exports/preview', {
            source: source.key,
            filter: clean,
            columns,
            page,
            page_size: pageSize,
          })
          if (id !== reqId.current) return
          setPreview(r)
          setPreviewErr('')
        } catch (e) {
          if (id === reqId.current) setPreviewErr(errText(e, t))
        } finally {
          if (id === reqId.current) setLoading(false)
        }
      },
      filterChanged ? DEBOUNCE_MS : 0,
    )
    return () => window.clearTimeout(handle)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source?.key, cleanKey, columns, page, pageSize, t])

  // --- handlers ----------------------------------------------------------------------------
  const setFilter = (f: ExportFilter) => {
    setFilterState(f)
    setPage(1)
  }
  const chooseSource = (key: string) => {
    const next = sources?.find((s) => s.key === key)
    if (!next || key === sourceKey) return
    setSourceKey(key)
    setFilterState(emptyFilter())
    setMode('filter')
    setModeHint(false)
    setColumns(loadColumns(key, next.columns))
    setPage(1)
    setPreview(null)
  }
  const changeMode = (m: Mode) => {
    if (m === 'filter') {
      if (!isChipExpressible(clean)) {
        setModeHint(true)
        return
      }
      setFilterState(clean)
    }
    setModeHint(false)
    setMode(m)
  }
  const saveCols = (keys: string[]) => {
    setColumns(keys)
    if (source) saveColumns(source.key, keys)
    setColsOpen(false)
  }
  const created = (job: ExportJob) => {
    setExportOpen(false)
    notifyExportsChanged({ job })
    const key = `export-${job.id}`
    notification.success({
      key,
      title: t('export.created.title'),
      description: t('export.created.body', { name: job.name || labels.source(job.source) }),
      actions: (
        <Button
          size="small"
          type="primary"
          onClick={() => {
            notifyExportsChanged({ open: true })
            notification.destroy(key)
          }}
        >
          {t('export.created.view')}
        </Button>
      ),
    })
  }
  const reuse = (job: ExportJob) => {
    const next = new URLSearchParams({ source: job.source })
    if (job.filter?.items?.length) next.set('f', encodeFilter(job.filter))
    if (sources) applyUrl(sources, next)
    window.scrollTo?.({ top: 0, behavior: 'smooth' })
  }

  // --- render ------------------------------------------------------------------------------
  const treeField = source?.fields.find((f) => f.tree)
  const rail = source && sources && (
    <Space orientation="vertical" size={16} style={{ width: '100%' }}>
      <SourceRail sources={sources} value={source.key} onChange={chooseSource} labels={labels} compact={compact} />
      {treeField?.tree && (() => {
        const tree = (
          <TypeTree
            tree={treeField.tree}
            selection={selectionOf(filter)}
            disabled={filter.op === 'or' && filter.items.length > 1}
            onChange={(sel) => setFilter(applySelection(filter, sel))}
          />
        )
        return compact ? <Collapse size="small" items={[{ key: 'tree', label: t('export.tree.title'), children: tree }]} /> : (
          <div>
            <Typography.Text type="secondary" style={{ fontSize: 12, textTransform: 'uppercase', letterSpacing: 0.4, display: 'block', padding: '0 8px 8px' }}>
              {t('export.tree.title')}
            </Typography.Text>
            {tree}
          </div>
        )
      })()}
    </Space>
  )

  return (
    <LoadGate loading={!sources && !loadErr} error={loadErr} onRetry={load} title={t('export.loadFailed')}>
      {source && (
        <Space orientation="vertical" size={16} style={{ width: '100%' }}>
          <div style={{ display: 'grid', gridTemplateColumns: compact ? '1fr' : '260px minmax(0, 1fr)', gap: compact ? 16 : 24, alignItems: 'start' }}>
            <aside style={compact ? undefined : { position: 'sticky', top: 16, background: token.colorBgContainer, border: `1px solid ${token.colorBorderSecondary}`, borderRadius: token.borderRadiusLG, padding: 12 }}>
              {rail}
            </aside>
            <main style={{ display: 'flex', flexDirection: 'column', gap: 16, minWidth: 0 }}>
              <Card
                title={
                  <Space size={12} wrap>
                    <span>{labels.source(source.key)}</span>
                    <Typography.Text type="secondary" style={{ fontWeight: 400, fontSize: 13 }}>
                      {t('export.totalRows', { n: source.total.toLocaleString() })}
                    </Typography.Text>
                  </Space>
                }
                extra={
                  <Segmented<Mode>
                    value={mode}
                    onChange={changeMode}
                    options={[
                      { value: 'filter', label: t('export.mode.filter') },
                      { value: 'advanced', label: t('export.mode.advanced') },
                    ]}
                  />
                }
              >
                {modeHint && <Alert type="info" showIcon closable onClose={() => setModeHint(false)} title={t('export.mode.advancedOnly')} style={{ marginBottom: 12 }} />}
                {mode === 'filter' ? (
                  <FilterBar source={source} filter={filter} onChange={setFilter} labels={labels} today={today} />
                ) : (
                  <Space orientation="vertical" size={12} style={{ width: '100%' }}>
                    <ConditionBuilder source={source} filter={filter} onChange={setFilter} labels={labels} />
                    {filter.items.length > 0 && (
                      <Button type="link" style={{ padding: 0 }} onClick={() => setFilter(emptyFilter())}>
                        {t('export.filter.clear')}
                      </Button>
                    )}
                  </Space>
                )}
              </Card>

              <Card>
                <ResultsPanel
                  source={source}
                  columns={columns}
                  rows={preview?.rows ?? []}
                  total={preview ? preview.total : null}
                  loading={loading}
                  error={previewErr}
                  page={page}
                  pageSize={pageSize}
                  tz={tz}
                  labels={labels}
                  onPage={(p, s) => {
                    setPage(s !== pageSize ? 1 : p)
                    setPageSize(s)
                  }}
                  onManageColumns={() => setColsOpen(true)}
                  onExportAll={() => setExportOpen(true)}
                />
              </Card>

              <ExportHistory tz={tz} labels={labels} onReuse={reuse} />
            </main>
          </div>

          <ColumnsDialog open={colsOpen} source={source} value={columns} labels={labels} onCancel={() => setColsOpen(false)} onSave={saveCols} />
          <ExportDialog
            open={exportOpen}
            source={source}
            filter={clean}
            columns={columns}
            total={preview?.total ?? 0}
            labels={labels}
            defaultName={`${labels.source(source.key)} ${today}`}
            onCancel={() => setExportOpen(false)}
            onCreated={created}
          />
        </Space>
      )}
    </LoadGate>
  )
}
