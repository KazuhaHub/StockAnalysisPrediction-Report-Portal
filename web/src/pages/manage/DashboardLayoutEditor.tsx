import { createPortal } from 'react-dom'
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Alert, App, Button, Checkbox, Collapse, Modal, Empty, Input, Select, Switch, Space, Typography } from 'antd'
import { CloseOutlined, HolderOutlined, PlusOutlined, SettingOutlined, MoreOutlined } from '@ant-design/icons'
import { DndContext, DragOverlay, KeyboardSensor, PointerSensor, AutoScrollActivator, MeasuringStrategy, useSensor, useSensors } from '@dnd-kit/core'
import { SortableContext, arrayMove, sortableKeyboardCoordinates, useSortable } from '@dnd-kit/sortable'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import DashboardWidgetPreview from './DashboardWidgetPreview'
import { ADMIN_GROUPS } from './adminNavigation'
import { usePendingSettings } from './AdminDraftBoundary'
import { DASHBOARD_METRICS, DEFAULT_METRICS } from './dashboardMetrics'
import { animateDashboardChange, animateDashboardModeChange } from './dashboardMotion'
import { dashboardCollision, snapshotDashboardWidget } from './dashboardDrag'
import { defaultDashboardChart, isDashboardChart, dashboardBlockLabel, dashboardBlockCategory, dashboardCatalog, defaultDashboardBlocks, type DashboardBlock, type DashboardLayout } from './dashboardLayout'

function SizeLabel({ width }: { width: DashboardBlock['width'] }) {
  const { t } = useTranslation()
  const label = width === 1 ? 'miniSize' : width === 2 ? 'smallSize' : width === 3 ? 'mediumSize' : 'largeSize'
  return <span className="rp-dashboard-size-label"><span className="rp-dashboard-size-diagram" aria-hidden="true">
    <span style={{ width: `${width / 6 * 100}%` }} />
  </span><span>{t(`manage.dashboard.${label}`)}</span></span>
}

function Widget({ block, editing, renderBlock, onResize, onRemove, onMetrics, dragHeight }: { dragHeight?: number; block: DashboardBlock; editing: boolean
  renderBlock: (block: DashboardBlock) => ReactNode; onResize: (width: DashboardBlock['width']) => void; onRemove: () => void; onMetrics: () => void }) {
  const { t } = useTranslation()
  const name = t(dashboardBlockLabel(block.id))
  const customizeLabel = t(isDashboardChart(block.id) ? 'manage.dashboard.customizeChart' : 'manage.dashboard.chooseMetrics', { name })
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, isDragging, isOver } = useSortable({ id: block.id, disabled: !editing })
  return <div ref={setNodeRef} {...(editing ? attributes : {})} {...(editing ? listeners : {})}
    role={editing ? 'group' : undefined} aria-label={editing ? t('manage.dashboard.moveBlock', { name }) : undefined}
    className={`rp-dashboard-block rp-dashboard-width-${block.width} ${['reports', 'queue', 'forecast'].includes(block.id) ? 'rp-dashboard-summary' : ''} ${editing ? 'rp-dashboard-editable' : ''} ${isOver ? 'rp-dashboard-drop-target' : ''}`}
    data-dashboard-block={block.id} style={{ gridColumn: `span ${block.width}`, height: isDragging ? dragHeight : undefined, opacity: isDragging ? 0.3 : 1 } as React.CSSProperties}>
    {editing && <div className="rp-dashboard-widget-tools">
      <Button type="text" size="small" ref={setActivatorNodeRef} icon={<HolderOutlined />} {...attributes} {...listeners}
        className="rp-dashboard-drag-handle" aria-label={t('manage.dashboard.moveBlock', { name })}
        onPointerDown={(event) => { event.stopPropagation(); listeners?.onPointerDown?.(event) }}
        onKeyDown={(event) => { if (!isDragging) { event.stopPropagation(); listeners?.onKeyDown?.(event) } }} />
      <Typography.Text ellipsis>{name}</Typography.Text>
      <div className="rp-dashboard-widget-controls" onPointerDown={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <Select size="small" aria-label={t('manage.dashboard.blockWidth', { name })} value={block.width} onChange={onResize} options={[
        ...(block.id.startsWith('shortcut:') ? [{ value: 1, label: <SizeLabel width={1} /> }] : []),
        { value: 2, label: <SizeLabel width={2} /> }, { value: 3, label: <SizeLabel width={3} /> }, { value: 6, label: <SizeLabel width={6} /> },
      ]} />
      {(DASHBOARD_METRICS[block.id] || isDashboardChart(block.id)) && <Button size="small" icon={<MoreOutlined />} title={customizeLabel} aria-label={customizeLabel} onClick={onMetrics} />}
      <Button type="text" size="small" icon={<CloseOutlined />} aria-label={t('manage.dashboard.removeBlock', { name })} onClick={onRemove} />
      </div>
    </div>}
    <div className="rp-dashboard-widget-content" inert={editing || undefined}>{renderBlock(block)}</div>
  </div>
}

function FrozenWidget({ preview }: { preview: ReturnType<typeof snapshotDashboardWidget> }) {
  const host = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => { host.current?.replaceChildren(preview.node) }, [preview])
  return <div ref={host} className="rp-dashboard-drag-preview" aria-hidden="true" inert style={{ width: preview.width, height: preview.height }} />
}

export default function DashboardLayoutEditor({ layout, error, onSaved, onPreview, renderBlock }: {
  layout: DashboardLayout | null; error: string; onSaved: (next: DashboardLayout) => void
  onPreview: (blocks: DashboardBlock[] | null) => void; renderBlock: (block: DashboardBlock) => ReactNode
}) {
  const { t } = useTranslation()
  const { modal } = App.useApp()
  const [headerSlot, setHeaderSlot] = useState<HTMLElement | null>(null)
  useEffect(() => { setHeaderSlot(document.getElementById('rp-admin-page-actions')) }, [])
  const [base, setBase] = useState<DashboardLayout | null>(null)
  const [draft, setDraft] = useState<DashboardBlock[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [palette, setPalette] = useState(false)
  const [search, setSearch] = useState('')
  const [paletteGroups, setPaletteGroups] = useState<string[]>(['metrics','charts'])
  const [chartBlock, setChartBlock] = useState<string | null>(null)
  const [metricBlock, setMetricBlock] = useState<string | null>(null)
  const [dragPreview, setDragPreview] = useState<ReturnType<typeof snapshotDashboardWidget> | null>(null)
  const dragStartBlocks = useRef<DashboardBlock[] | null>(null)
  const dragActiveId = useRef<string | null>(null)
  const dragLastMove = useRef(0)
  const gridRef = useRef<HTMLDivElement>(null)
  const gridPositions = useRef(new Map<string, { x: number; y: number }>())
  useLayoutEffect(() => {
    if (!dragPreview) { gridPositions.current.clear(); return }
    gridRef.current?.querySelectorAll<HTMLElement>('[data-dashboard-block]').forEach(element => {
      const id = element.dataset.dashboardBlock!
      const previous = gridPositions.current.get(id)
      const next = { x: element.offsetLeft, y: element.offsetTop }
      gridPositions.current.set(id, next)
      if (previous && id !== dragActiveId.current &&
        (previous.x !== next.x || previous.y !== next.y) && !window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
        element.getAnimations?.().forEach(animation => animation.cancel())
        element.animate?.([{ transform: `translate(${previous.x - next.x}px, ${previous.y - next.y}px)` }, { transform: 'none' }], { duration: 160, easing: 'ease-out' })
      }
    })
  }, [draft, dragPreview])
  const [addedId, setAddedId] = useState<string | null>(null)
  const editing = base !== null
  const blocks = editing ? draft : layout?.blocks ?? defaultDashboardBlocks()
  const visible = blocks.filter((block) => block.visible && dashboardCatalog().some((item) => item.id === block.id))
  const dirty = editing && JSON.stringify(draft) !== JSON.stringify(base.blocks)
  usePendingSettings(dirty, saving)
  useEffect(() => { onPreview(editing ? draft : null) }, [draft, editing, onPreview])
  useEffect(() => () => onPreview(null), [onPreview])
  useEffect(() => {
    if (!addedId || palette) return
    const frame = requestAnimationFrame(() => {
      const element = document.querySelector<HTMLElement>(`[data-dashboard-block="${addedId}"]`)
      element?.scrollIntoView?.({ block: 'nearest' })
      element?.focus()
      setAddedId(null)
    })
    return () => cancelAnimationFrame(frame)
  }, [addedId, palette])
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 8 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }))
  const stop = () => animateDashboardModeChange(() => { setBase(null); setSaveError(''); setPalette(false) }, gridRef.current)
  const start = () => animateDashboardModeChange(() => { setBase(layout); setDraft(layout!.blocks) }, gridRef.current)
  const cancel = () => {
    if (dirty) modal.confirm({ title: t('manage.leaveTitle'), content: t('manage.leaveDescription'), okText: t('manage.discard'), cancelText: t('manage.stay'), onOk: stop })
    else stop()
  }
  const save = async () => {
    if (!base) return
    setSaving(true); setSaveError('')
    try { const next = await api.put<DashboardLayout>('/api/admin/dashboard/layout', { blocks: draft, revision: base.revision }); onSaved(next); stop() }
    catch (e) { setSaveError(errText(e, t)) }
    finally { setSaving(false) }
  }
  const blockName = (id: string | number) => t(dashboardBlockLabel(String(id)))
  return <>
    {headerSlot ? createPortal(<div className="rp-dashboard-header-tools">{editing ? <Space wrap>
      <Button icon={<PlusOutlined />} disabled={saving} onClick={() => setPalette(true)}>{t('manage.dashboard.addBlock')}</Button>
      <Button data-dashboard-mode-toggle disabled={saving} onClick={cancel}>{t('common.cancel')}</Button>
      <Button type="primary" loading={saving} disabled={!dirty || !visible.length} onClick={save}>{t('manage.dashboard.saveGlobal')}</Button>
    </Space> : <Button data-dashboard-mode-toggle icon={<SettingOutlined />} disabled={!layout} onClick={start}>{t('manage.dashboard.customize')}</Button>}</div>, headerSlot) : null}
    <div className="rp-dashboard-layout-tools" hidden={!!headerSlot && !editing}>
      {editing ? <>
        <Typography.Text type="secondary">{t('manage.dashboard.directHint')}</Typography.Text>{headerSlot && <Button size="small" onClick={() => animateDashboardChange(() => setDraft(defaultDashboardBlocks()))}>{t('table.reset')}</Button>}
        <Space wrap style={headerSlot ? { display: 'none' } : undefined}>
          <Button icon={<PlusOutlined />} disabled={saving} onClick={() => setPalette(true)}>{t('manage.dashboard.addBlock')}</Button>
          <Button disabled={saving} onClick={() => animateDashboardChange(() => setDraft(defaultDashboardBlocks()))}>{t('table.reset')}</Button>
          <Button data-dashboard-mode-toggle disabled={saving} onClick={cancel}>{t('common.cancel')}</Button>
          <Button type="primary" loading={saving} disabled={!dirty || !visible.length} onClick={save}>{t('manage.dashboard.saveGlobal')}</Button>
        </Space>
      </> : <Button data-dashboard-mode-toggle icon={<SettingOutlined />} disabled={!layout} onClick={start}>{t('manage.dashboard.customize')}</Button>}
    </div>
    {(error || saveError) && <Alert type="error" showIcon title={saveError || error} />}
    {!visible.length && <Empty description={t('manage.dashboard.needBlock')} />}
    <DndContext sensors={sensors} collisionDetection={dashboardCollision}
      measuring={{ droppable: { strategy: MeasuringStrategy.Always } }}
      autoScroll={{ activator: AutoScrollActivator.Pointer, threshold: { x: 0, y: 0.18 }, acceleration: 12,
        layoutShiftCompensation: false, canScroll: (element) => element.classList.contains('rp-admin-page-body') }}
      accessibility={{ screenReaderInstructions: { draggable: t('manage.dashboard.dragInstructions') }, announcements: {
        onDragStart: ({ active }) => t('manage.dashboard.dragStart', { name: blockName(active.id) }),
        onDragOver: ({ over }) => over ? t('manage.dashboard.dragOver', { name: blockName(over.id) }) : undefined,
        onDragEnd: () => t('manage.dashboard.dragDone'), onDragCancel: () => t('manage.dashboard.dragCancel'),
      } }}
      onDragStart={({ active }) => {
        const source = document.querySelector<HTMLElement>(`[data-dashboard-block="${active.id}"]`)
        dragActiveId.current = String(active.id)
        dragStartBlocks.current = draft
        dragLastMove.current = 0
        if (source) {
          source.getAnimations?.().forEach((animation) => animation.cancel())
          setDragPreview(snapshotDashboardWidget(source))
        }
      }} onDragCancel={() => {
        if (dragStartBlocks.current) setDraft(dragStartBlocks.current)
        dragStartBlocks.current = null
        setDragPreview(null)
      }}
      onDragOver={({ active, over }) => {
        if (!over || active.id === over.id || performance.now() - dragLastMove.current < 100) return
        dragLastMove.current = performance.now()
        setDraft(previous => arrayMove(previous, previous.findIndex(b => b.id === active.id), previous.findIndex(b => b.id === over.id)))
      }}
      onDragEnd={() => {
        dragStartBlocks.current = null
        setDragPreview(null)
      }}>
      <SortableContext items={visible.map((block) => block.id)} strategy={() => null}>
        <div ref={gridRef} className={`rp-dashboard-grid ${editing ? 'rp-dashboard-editing' : ''}`}>{visible.map((block) => <Widget key={block.id} block={block} editing={editing} renderBlock={renderBlock} dragHeight={dragPreview?.height}
          onResize={(width) => animateDashboardChange(() => setDraft((previous) => previous.map((item) => item.id === block.id ? { ...item, width } : item)))}
          onMetrics={() => isDashboardChart(block.id) ? setChartBlock(block.id) : setMetricBlock(block.id)}
          onRemove={() => animateDashboardChange(() => setDraft((previous) => previous.map((item) => item.id === block.id ? { ...item, visible: false } : item)))} />)}</div>
      </SortableContext>
      {createPortal(<DragOverlay adjustScale={false} dropAnimation={null}>{dragPreview && <FrozenWidget preview={dragPreview} />}</DragOverlay>, document.querySelector('.ant-app') ?? document.body)}
    </DndContext>
    <Modal open={!!chartBlock} title={t('manage.dashboard.chartOptions')} onCancel={() => setChartBlock(null)} footer={<Button type="primary" onClick={() => setChartBlock(null)}>{t('common.done')}</Button>}>
      {chartBlock && (() => {
        const config = draft.find(b => b.id === chartBlock)?.chart ?? defaultDashboardChart()
        const update = (change: Partial<typeof config>) => setDraft(previous => previous.map(b => b.id === chartBlock ? { ...b, chart: { ...config, ...change } } : b))
        const series = chartBlock === 'trend' || chartBlock === 'report-bars'
        return <Space orientation="vertical" size={18} style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('manage.dashboard.chartOptionsHint')}</Typography.Text>
          <label>{t('manage.dashboard.chartRange')}<Select aria-label={t('manage.dashboard.chartRange')} value={config.range} style={{ display: 'block', width: '100%', marginTop: 8 }} onChange={range => update({range})} options={([7,30,90,365] as const).map(value => ({ value, label: t(`manage.dashboard.range.${value}`) }))} /></label>
          {series ? <>
            <label>{t('manage.dashboard.chartInterval')}<Select aria-label={t('manage.dashboard.chartInterval')} value={config.interval} style={{ display: 'block', width: '100%', marginTop: 8 }} onChange={interval => update({interval})} options={(['auto','day','week','month'] as const).map(value => ({ value, label: t(`manage.dashboard.interval.${value}`) }))} /></label>
            <Space><Switch checked={config.cumulative} onChange={cumulative => update({cumulative})} aria-label={t('manage.dashboard.cumulative')} />{t('manage.dashboard.cumulative')}</Space>
          </> : <label>{t('manage.dashboard.topCategories')}<Select aria-label={t('manage.dashboard.topCategories')} value={config.topN} style={{ marginInlineStart: 12, minWidth: 80 }} onChange={topN => update({topN})} options={([5,10,15] as const).map(value => ({ value, label: String(value) }))} /></label>}
          <Space><Switch checked={config.showValues} onChange={showValues => update({showValues})} aria-label={t('manage.dashboard.showValues')} />{t('manage.dashboard.showValues')}</Space>
          <Typography.Text type="secondary">{t('manage.dashboard.chartDensityHint')}</Typography.Text>
        </Space>
      })()}
    </Modal>
    <Modal open={!!metricBlock} title={t('manage.dashboard.metricPickerTitle')} onCancel={() => setMetricBlock(null)} footer={<Button type="primary" onClick={() => setMetricBlock(null)}>{t('common.done')}</Button>}>
      <Typography.Paragraph type="secondary">{t('manage.dashboard.metricPickerHint')}</Typography.Paragraph>
      <Space orientation="vertical" style={{ width: '100%' }}>{metricBlock && DASHBOARD_METRICS[metricBlock].map((definition) => {
        const chosen = draft.find((block) => block.id === metricBlock)?.metrics ?? DEFAULT_METRICS[metricBlock]
        return <Checkbox key={definition.key} checked={chosen.includes(definition.key)} disabled={chosen.length === 1 && chosen.includes(definition.key)} onChange={(event) => {
          animateDashboardChange(() => setDraft((previous) => previous.map((block) => block.id === metricBlock ? { ...block, metrics: event.target.checked ? [...chosen, definition.key] : chosen.filter((key) => key !== definition.key) } : block)))
        }}>{t(`manage.dashboard.${definition.label}`)}</Checkbox>
      })}</Space>
    </Modal>
    <Modal open={palette} title={t('manage.dashboard.addBlock')} onCancel={() => setPalette(false)} footer={null} width="min(1200px, calc(100vw - 48px))" centered className="rp-dashboard-catalog-modal" styles={{ body: { maxHeight: 'calc(100dvh - 180px)', overflow: 'auto', paddingInlineEnd: 12 } }}>
      <Input.Search aria-label={t('manage.dashboard.findBlock')} placeholder={t('manage.dashboard.findBlock')} value={search} onChange={(event) => setSearch(event.target.value)} />
      <Collapse className="rp-dashboard-palette-groups" items={[
        ...(['metrics','charts','features'] as const).map((category) => ({ key: category, label: t(`manage.dashboard.category.${category}`), children: <div className="rp-dashboard-preview-grid">{dashboardCatalog().filter((item) => !item.id.startsWith('shortcut:') && dashboardBlockCategory(item.id) === category && t(item.label).toLocaleLowerCase().includes(search.toLocaleLowerCase())).sort((a, b) => Number(!['trend','kinds','report-bars','report-donut'].includes(a.id)) - Number(!['trend','kinds','report-bars','report-donut'].includes(b.id))).map((item) => {
          const added = draft.some((block) => block.id === item.id && block.visible)
          return <div className="rp-dashboard-catalog-card" key={item.id}>
            <DashboardWidgetPreview id={item.id} />
            <Button block disabled={added} icon={<PlusOutlined />} onClick={() => {
              animateDashboardChange(() => { setDraft((previous) => [...previous.filter((block) => block.id !== item.id), { ...previous.find((block) => block.id === item.id), id: item.id, width: previous.find((block) => block.id === item.id)?.width ?? item.width, visible: true }]); setAddedId(item.id); setPalette(false) })
            }}>{t(item.label)}{added && ` · ${t('manage.dashboard.added')}`}</Button>
          </div>
        })}</div> })),
        ...ADMIN_GROUPS.filter((group) => !search || group.pages.some((page) => t(page.label).toLocaleLowerCase().includes(search.toLocaleLowerCase()))).map((group) => ({ key: group.key, label: `${t('manage.dashboard.shortcutBlocks')} · ${t(group.label)}`, children: <div className="rp-dashboard-shortcut-options">{group.pages.filter((page) => t(page.label).toLocaleLowerCase().includes(search.toLocaleLowerCase())).map((page) => {
          const id = `shortcut:${page.key}`
          const added = draft.some((block) => block.id === id && block.visible)
          return <Button key={id} disabled={added} icon={page.icon} onClick={() => {
            animateDashboardChange(() => { setDraft((previous) => [...previous.filter((block) => block.id !== id), { ...previous.find((block) => block.id === id), id, width: previous.find((block) => block.id === id)?.width ?? 1, visible: true }]); setAddedId(id); setPalette(false) })
          }}>{t(page.label)}{added && ` · ${t('manage.dashboard.added')}`}</Button>
        })}</div> })),
      ]} activeKey={search ? ['metrics','charts','features', ...ADMIN_GROUPS.map((group) => group.key)] : paletteGroups} onChange={(keys) => setPaletteGroups(Array.isArray(keys) ? keys : [keys])} />
    </Modal>
  </>
}
