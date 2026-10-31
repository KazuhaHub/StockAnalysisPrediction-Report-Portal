import { useContext, useEffect, useMemo, useRef, useState } from 'react'
import { Button, Input, Modal, Pagination, Tooltip, theme, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { init, use as registerCharts, type EChartsType } from 'echarts/core'
import { LineChart, BarChart, PieChart, type LineSeriesOption, type BarSeriesOption, type PieSeriesOption } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent, TitleComponent } from 'echarts/components'
import { SVGRenderer } from 'echarts/renderers'
import type { EChartsOption } from 'echarts'
import { createPortal } from 'react-dom'
import { DownloadOutlined, TableOutlined } from '@ant-design/icons'
import { downloadCSV } from '../../lib/csv'
import { ChartHeaderTarget } from './DashboardChartCard'
import { chartDataCSV } from './dashboardChartExport'
import { RADIUS } from '../../theme/radius'

registerCharts([LineChart, BarChart, PieChart, GridComponent, TooltipComponent, LegendComponent, TitleComponent, SVGRenderer])
export interface ChartRow { name: string; value: number }
interface ChartProps {
  label: string
  rows: ChartRow[]
  series: LineSeriesOption | BarSeriesOption | PieSeriesOption
  horizontal?: boolean
  dates?: boolean
  total?: number
  hint?: string
  legendValues?: boolean
}

/** Container sizing belongs to the dashboard; plot geometry and interactions belong to ECharts. */
export default function DashboardChart({ label, rows, series, horizontal, dates, total, hint, legendValues }: ChartProps) {
  const { token } = theme.useToken()
  const { t } = useTranslation()
  const host = useRef<HTMLDivElement>(null)
  const headerTarget = useContext(ChartHeaderTarget)
  const [snapshot, setSnapshot] = useState<{rows: ChartRow[]; total?: number} | null>(null)
  const [query, setQuery] = useState('')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(25)
  const filtered = useMemo(() => (snapshot?.rows ?? []).filter(row => row.name.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase())), [snapshot, query])
  const headers = [t('manage.dashboard.dataLabel'), t('manage.dashboard.dataCount'), ...(snapshot?.total === undefined ? [] : [t('manage.dashboard.dataShare')])]
  const openData = () => { setSnapshot({rows:rows.map(row => ({...row})), total}); setQuery(''); setPage(1) }
  const dataButton = <Tooltip title={t('manage.dashboard.viewChartData')}><Button size="small" icon={<TableOutlined aria-hidden="true" />} aria-label={t('manage.dashboard.viewChartData')} onClick={openData} /></Tooltip>
  const refresh = useRef<() => void>(() => {})
  const pie = series.type === 'pie'
  const summary = rows.map(item => `${item.name}: ${item.value}`).join(', ')
  const makeOption = useMemo(() => (width: number): EChartsOption => {
    const narrow = width < 460
    const countLabel = (value: number) => t('manage.dashboard.tooltipCount', { count: value })
    const tooltip = (raw: unknown) => {
      const item = (Array.isArray(raw) ? raw[0] : raw) as { name?: string; value?: number; marker?: string }
      const value = Number(item?.value ?? 0)
      return [item?.name ?? '', countLabel(value), ...(total === undefined ? [] : [t('manage.dashboard.tooltipShare', { percent: total ? (value / total * 100).toFixed(1) : '0.0' })])].join('\n')
    }
    const category = {
      type: 'category' as const, data: rows.map(item => item.name), inverse: !!horizontal,
      axisLabel: { color: token.colorTextSecondary, hideOverlap: true, overflow: 'truncate' as const,
        width: horizontal ? Math.min(130, width * .3) : undefined,
        formatter: dates ? (name: string) => name.slice(5) : undefined },
      axisLine: { lineStyle: { color: token.colorBorderSecondary } }, axisTick: { show: false },
    }
    const value = { type: 'value' as const, minInterval: 1, axisLabel: { color: token.colorTextSecondary }, splitLine: { lineStyle: { color: token.colorBorderSecondary, type: 'dashed' as const } } }
    return {
      animationDuration: 250, animationDurationUpdate: 180,
      animation: !window.matchMedia('(prefers-reduced-motion: reduce)').matches,
      color: [token.colorPrimary, token.colorSuccess, token.colorWarning, token.colorInfo, token.colorError, token.colorTextTertiary],
      textStyle: { fontFamily: token.fontFamily, fontSize: 12, color: token.colorText },
      tooltip: { trigger: pie || horizontal ? 'item' : 'axis', renderMode: 'richText', confine: true, formatter: tooltip,
        backgroundColor: token.colorBgElevated, borderColor: token.colorBorder, textStyle: { color: token.colorText, fontSize: 12 } },
      ...(pie ? {
        legend: { type: 'scroll', orient: narrow ? 'horizontal' : 'vertical', ...(narrow ? { bottom: 0, left: 'center' } : { left: '54%', right: 8, top: 'middle' }),
          formatter: legendValues ? (name: string) => `${name}  ${rows.find(row => row.name === name)?.value ?? 0}` : undefined,
          textStyle: { color: token.colorTextSecondary, width: narrow ? 110 : Math.max(80, width * .4), overflow: 'truncate' }, tooltip: { show: true } },
        title: { text: rows.length ? String(total ?? 0) : t('manage.dashboard.noReports'), left: narrow ? 'center' : '25%', top: narrow ? '34%' : '44%', textAlign: 'center',
          textStyle: { color: token.colorText, fontSize: 20, fontWeight: 600 } },
      } : {
        grid: { left: 8, right: horizontal ? 45 : 16, top: 24, bottom: 8, containLabel: true },
        xAxis: horizontal ? value : category, yAxis: horizontal ? category : value,
      }),
      series: [{ ...series, ...(pie ? { center: narrow ? ['50%', '40%'] : ['26%', '50%'], radius: narrow ? ['35%', '53%'] : ['45%', '68%'], stillShowZeroSum: false } : {}),
        data: pie ? rows : rows.map(item => item.value), label: { ...series.label, color: token.colorText, fontSize: 12 },
        emphasis: { focus: 'self' }, ...(pie ? {} : { itemStyle: { borderRadius: RADIUS.xs } }),
      }],
    } as EChartsOption
  }, [rows, series, horizontal, dates, total, pie, legendValues, token, t])
  const latest = useRef(makeOption)
  latest.current = makeOption
  useEffect(() => {
    const element = host.current!
    let chart: EChartsType | undefined
    let frame = 0
    const draw = () => {
      const { width, height } = element.getBoundingClientRect()
      if (!width || !height) return
      chart ??= init(element, undefined, { renderer: 'svg' })
      chart.resize({ width, height })
      chart.setOption(latest.current(width), { notMerge: true })
    }
    refresh.current = draw
    const observer = new ResizeObserver(() => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(draw)
    })
    observer.observe(element)
    draw()
    return () => { refresh.current = () => {}; cancelAnimationFrame(frame); observer.disconnect(); chart?.dispose() }
  }, [])
  useEffect(() => { refresh.current() }, [makeOption])
  return <figure className="rp-dashboard-chart rp-dashboard-echart">
    <div role="img" aria-label={label} className="rp-dashboard-chart-visual">
      <span className="rp-sr-only">{summary || t('manage.dashboard.noReports')}</span>
      <div ref={host} aria-hidden="true" className="rp-dashboard-echart-host" style={{ minHeight: horizontal ? Math.max(220, rows.length * 30 + 40) : undefined }} />
    </div>
    {hint && <figcaption><Typography.Text type="secondary">{hint}</Typography.Text></figcaption>}
    {headerTarget && createPortal(dataButton, headerTarget)}
    <Modal open={snapshot !== null} onCancel={() => setSnapshot(null)} footer={null} title={label} width={760}
      className="rp-dashboard-chart-data-modal" styles={{ body: { maxHeight: '65dvh', overflow: 'auto' } }}>
      {hint && <Typography.Paragraph type="secondary">{hint}</Typography.Paragraph>}
      <div className="rp-dashboard-data-toolbar"><Input.Search allowClear value={query} placeholder={t('manage.dashboard.searchChartData')}
        aria-label={t('manage.dashboard.searchChartData')} onChange={event => {setQuery(event.target.value); setPage(1)}} />
        <Button icon={<DownloadOutlined aria-hidden="true" />} disabled={!filtered.length} onClick={() => downloadCSV(`chart-data-${new Date().toISOString().slice(0,10)}.csv`,chartDataCSV(headers,filtered,snapshot?.total))}>{t('manage.dashboard.downloadChartCSV')}</Button>
      </div>
      <Typography.Paragraph type="secondary">{t('manage.dashboard.chartExportHint')}</Typography.Paragraph>
      <div className="rp-dashboard-data-scroll"><table><caption className="rp-sr-only">{label}</caption>
        <thead><tr><th scope="col">{t('manage.dashboard.dataLabel')}</th><th scope="col">{t('manage.dashboard.dataCount')}</th>{snapshot?.total !== undefined && <th scope="col">{t('manage.dashboard.dataShare')}</th>}</tr></thead>
        <tbody>{filtered.slice((page-1)*pageSize,page*pageSize).map((item,index) => <tr key={`${item.name}:${index}`}><th scope="row">{item.name}</th><td>{item.value}</td>{snapshot?.total !== undefined && <td>{snapshot.total ? (item.value / snapshot.total * 100).toFixed(1) : '0.0'}%</td>}</tr>)}</tbody>
      </table></div>
      <div className="rp-dashboard-data-pagination"><Typography.Text type="secondary">{t('manage.dashboard.chartDataRows',{count:filtered.length})}</Typography.Text><Pagination current={page} pageSize={pageSize} total={filtered.length} showSizeChanger pageSizeOptions={[25,50,100]} onChange={(next,size) => {setPage(size === pageSize ? next : 1); setPageSize(size)}} /></div>
      {!filtered.length && <Typography.Paragraph>{t('manage.dashboard.noReports')}</Typography.Paragraph>}
    </Modal>
  </figure>
}
