import type { ReactNode } from 'react'
import { Button, Checkbox, InputNumber } from 'antd'
import { ArrowDownOutlined, ArrowUpOutlined, HolderOutlined } from '@ant-design/icons'
import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors } from '@dnd-kit/core'
import { SortableContext, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { restrictToParentElement, restrictToVerticalAxis } from '@dnd-kit/modifiers'
import { CSS } from '@dnd-kit/utilities'
import { useTranslation } from 'react-i18next'

export type ColumnOption = { key: string; label: ReactNode; name: string; required: boolean; visible: boolean; width: number }
function ColumnRow({ option, first, last, onVisible, onWidth, onMove }: {
  option: ColumnOption; first: boolean; last: boolean
  onVisible: (key: string, visible: boolean) => void
  onWidth: (key: string, width: number) => void
  onMove: (key: string, direction: number) => void
}) {
  const { t } = useTranslation()
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id: option.key })
  return <div ref={setNodeRef} data-column-option={option.key} className="rp-table-column-option"
    style={{ transform: CSS.Translate.toString(transform), transition, position: 'relative', zIndex: isDragging ? 2 : undefined,
      background: 'var(--ant-color-bg-elevated)' }}>
    <Button ref={setActivatorNodeRef} type="text" size="small" icon={<HolderOutlined />} {...attributes} {...listeners}
      aria-label={t('table.reorderColumn', { name: option.name })} style={{ cursor: 'grab', touchAction: 'none' }} />
    <Checkbox disabled={option.required} checked={option.visible} onChange={(event) => onVisible(option.key, event.target.checked)}>{option.label}</Checkbox>
    <InputNumber size="small" min={64} max={1200} step={10} precision={0} value={option.width}
      aria-label={t('table.columnWidth', { name: option.name })} onChange={(value) => { if (value !== null) onWidth(option.key, Number(value)) }} />
    <div className="rp-table-column-move">
      <Button type="text" size="small" disabled={first} icon={<ArrowUpOutlined />} aria-label={t('table.moveColumnUp', { name: option.name })} onClick={() => onMove(option.key, -1)} />
      <Button type="text" size="small" disabled={last} icon={<ArrowDownOutlined />} aria-label={t('table.moveColumnDown', { name: option.name })} onClick={() => onMove(option.key, 1)} />
    </div>
  </div>
}
export default function TableColumnOptions({ options, onVisible, onWidth, onReorder }: {
  options: ColumnOption[]; onVisible: (key: string, visible: boolean) => void
  onWidth: (key: string, width: number) => void; onReorder: (from: string, to: string) => void
}) {
  const { t } = useTranslation()
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 5 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }))
  return <DndContext sensors={sensors} collisionDetection={closestCenter} modifiers={[restrictToVerticalAxis, restrictToParentElement]}
    accessibility={{ screenReaderInstructions: { draggable: t('table.reorderKeyboard') } }}
    onDragEnd={({ active, over }) => { if (over && active.id !== over.id) onReorder(String(active.id), String(over.id)) }}>
    <SortableContext items={options.map((option) => option.key)} strategy={verticalListSortingStrategy}>
      {options.map((option, index) => <ColumnRow key={option.key} option={option} first={index === 0} last={index === options.length - 1}
        onVisible={onVisible} onWidth={onWidth} onMove={(key, direction) => { const target = options[index + direction]; if (target) onReorder(key, target.key) }} />)}
    </SortableContext>
  </DndContext>
}
