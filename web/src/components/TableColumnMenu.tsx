import type { HTMLAttributes, KeyboardEvent, ReactNode, SyntheticEvent } from 'react'
import { Button, Checkbox, Divider, Dropdown, Switch, Typography } from 'antd'
import { MoreOutlined } from '@ant-design/icons'
import { DndContext, KeyboardSensor, MouseSensor, TouchSensor, closestCenter, useSensor, useSensors } from '@dnd-kit/core'
import { SortableContext, horizontalListSortingStrategy, sortableKeyboardCoordinates, useSortable } from '@dnd-kit/sortable'
import { restrictToHorizontalAxis } from '@dnd-kit/modifiers'
import { CSS } from '@dnd-kit/utilities'
import { useTranslation } from 'react-i18next'

export type ColumnOption = { key: string; label: ReactNode; required: boolean; visible: boolean }

// One menu for a table's columns, reached two ways like a spreadsheet's or the Cloudflare dashboard's:
// right-clicking any header opens it at the pointer, and a small ⋮ at the end of the header row opens it
// for touch and keyboard users. Order is changed by dragging the headers themselves.
export type ColumnMenuState = {
  options: ColumnOption[]
  fit: boolean
  /** Keys that can be dragged; pinned (fixed) columns stay where they are. */
  sortable: string[]
  onVisible: (key: string, visible: boolean) => void
  onFit: (fit: boolean) => void
  onReset: () => void
  onReorder: (from: string, to: string) => void
}

// The panel renders in a portal, but React still bubbles its events through the header cell that
// opened it: a click would sort that column, Space or a press-and-move would start dragging it, and a
// right-click would open a second menu over this one. None of them leaves the panel.
const stop = (event: SyntheticEvent) => event.stopPropagation()
const contained = { onClick: stop, onPointerDown: stop, onMouseDown: stop, onTouchStart: stop, onKeyDown: stop,
  onContextMenu: (event: SyntheticEvent) => { event.preventDefault(); event.stopPropagation() } }

export function ColumnMenuPanel({ state }: { state: ColumnMenuState }) {
  const { t } = useTranslation()
  return <div className="rp-column-menu" role="group" aria-label={t('table.display')} {...contained}>
    <Typography.Text strong>{t('table.display')}</Typography.Text>
    <div className="rp-column-menu-list">
      {state.options.map((option) => <Checkbox key={option.key} disabled={option.required} checked={option.visible}
        onChange={(event) => state.onVisible(option.key, event.target.checked)}>{option.label}</Checkbox>)}
    </div>
    <Divider style={{ margin: 0 }} />
    <label className="rp-table-view-fit"><span>{t('table.fit')}</span>
      <Switch size="small" checked={state.fit} onChange={state.onFit} /></label>
    <Typography.Text type="secondary" style={{ fontSize: 12 }}>{t('table.columnsHint')}</Typography.Text>
    <Button size="small" onClick={state.onReset}>{t('table.reset')}</Button>
  </div>
}

/** The ⋮ at the end of the header row. Its events stay here so they neither sort nor drag the column. */
// Open state is the hook's, not the Dropdown's: the ⋮ follows whichever column is last, so unticking
// that column remounts it elsewhere, and the menu has to stay open across the move.
export function ColumnMenuButton({ state, open, onOpenChange }: { state: ColumnMenuState; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation()
  return <span className="rp-column-menu-trigger">
    <Dropdown trigger={['click']} placement="bottomRight" open={open} onOpenChange={onOpenChange} popupRender={() => <ColumnMenuPanel state={state} />}>
      <Button type="text" size="small" icon={<MoreOutlined />} aria-label={t('table.columnMenu')} title={t('table.columnMenu')}
        {...contained} />
    </Dropdown>
  </span>
}

// antd renders the header through these components. They read the live state from a ref rather than
// taking it as props: the components must stay the same across renders, or every header cell would
// remount (and drop a drag in progress) on each change. A changed state always reaches them, because
// it arrives with new columns and so re-renders the header.
export type ColumnMenuRef = { current: ColumnMenuState }

type CellProps = HTMLAttributes<HTMLTableCellElement> & { 'data-column-key'?: string }

function SortableCell({ id, ...props }: CellProps & { id: string }) {
  const { t } = useTranslation()
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } =
    useSortable({ id, attributes: { roleDescription: t('table.movableColumn') } })
  // Keep the cell a column header for assistive technology: dnd-kit would make it a button, and
  // aria-pressed is not allowed on a column header.
  const { role: _role, 'aria-pressed': _pressed, ...dragAttributes } = attributes
  // Both handlers run: antd's (Enter sorts a sortable column) and dnd-kit's (Space picks it up).
  const onKeyDown = (event: KeyboardEvent<HTMLTableCellElement>) => { props.onKeyDown?.(event); listeners?.onKeyDown?.(event) }
  const ref = (node: HTMLTableCellElement | null) => { setNodeRef(node); setActivatorNodeRef(node) }
  return <th {...props} {...dragAttributes} {...listeners} onKeyDown={onKeyDown} ref={ref}
    data-dragging={isDragging || undefined}
    style={{ ...props.style, transform: CSS.Translate.toString(transform), transition, cursor: 'grab',
      ...(isDragging ? { position: 'relative', zIndex: 3, background: 'var(--ant-color-bg-elevated)', cursor: 'grabbing' } : null) }} />
}

export function makeColumnMenuComponents(live: ColumnMenuRef) {
  function HeaderWrapper(props: HTMLAttributes<HTMLTableSectionElement>) {
    const { t } = useTranslation()
    // A mouse drags after moving 5px, so a click still sorts; a finger after a short press, so a swipe
    // across the header still scrolls the table. Space only on the keyboard: Enter sorts.
    const sensors = useSensors(
      useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
      useSensor(TouchSensor, { activationConstraint: { delay: 250, tolerance: 5 } }),
      useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates, keyboardCodes: { start: ['Space'], cancel: ['Escape'], end: ['Space'] } }),
    )
    const state = live.current
    return <DndContext sensors={sensors} collisionDetection={closestCenter} modifiers={[restrictToHorizontalAxis]}
      accessibility={{ screenReaderInstructions: { draggable: t('table.reorderKeyboard') } }}
      onDragEnd={({ active, over }) => { if (over && active.id !== over.id) live.current.onReorder(String(active.id), String(over.id)) }}>
      <SortableContext items={state.sortable} strategy={horizontalListSortingStrategy}>
        <Dropdown trigger={['contextMenu']} popupRender={() => <ColumnMenuPanel state={live.current} />}>
          <thead {...props} />
        </Dropdown>
      </SortableContext>
    </DndContext>
  }
  function HeaderCell({ 'data-column-key': id, ...props }: CellProps) {
    if (id && live.current.sortable.includes(id)) return <SortableCell {...props} id={id} />
    return <th {...props} />
  }
  return { header: { wrapper: HeaderWrapper, cell: HeaderCell } }
}
