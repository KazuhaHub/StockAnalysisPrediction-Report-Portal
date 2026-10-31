import { useMemo, useState } from 'react'
import { Button, Checkbox, Empty, Input, Segmented, Typography, theme } from 'antd'
import { DownOutlined, RightOutlined, SearchOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import {
  kindCheckState,
  pickNode,
  toggleKind,
  toggleSubtype,
  type TypeSelection,
  type TypeTree as Tree,
} from '../../../lib/exportFilter'

// The report type tree (kind -> subtype) in the source rail, modelled on Workspace's organizational
// unit tree: search, a Single / Multi select toggle, checkboxes in multi mode. The selection is not
// state of its own; the page derives it from the filter and writes it back, so chips and tree agree.
export default function TypeTree({
  tree,
  selection,
  onChange,
  disabled,
}: {
  tree: Tree
  selection: TypeSelection
  onChange: (s: TypeSelection) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const [multi, setMulti] = useState(true)
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState<Record<string, boolean>>({})

  const q = query.trim().toLowerCase()
  const visible = useMemo(
    () =>
      tree
        .map((k) => {
          if (!q) return { ...k, subs: k.subtypes }
          if (k.kind.toLowerCase().includes(q)) return { ...k, subs: k.subtypes }
          return { ...k, subs: k.subtypes.filter((s) => s.toLowerCase().includes(q)) }
        })
        .filter((k) => !q || k.kind.toLowerCase().includes(q) || k.subs.length > 0),
    [tree, q],
  )

  const isOpen = (kind: string) => q !== '' || (open[kind] ?? (selection.kinds.includes(kind) ? false : tree.find((k) => k.kind === kind)?.subtypes.some((s) => selection.subtypes.includes(s)) ?? false))
  const toggleOpen = (kind: string) => setOpen((o) => ({ ...o, [kind]: !isOpen(kind) }))
  const none = selection.kinds.length === 0 && selection.subtypes.length === 0

  const rowStyle = (selected: boolean) => ({
    display: 'flex',
    alignItems: 'center',
    gap: 4,
    borderRadius: token.borderRadius,
    background: selected ? token.colorPrimaryBg : undefined,
  })

  return (
    <div aria-disabled={disabled} style={{ opacity: disabled ? 0.5 : 1, pointerEvents: disabled ? 'none' : undefined }}>
      <Input
        allowClear
        size="small"
        prefix={<SearchOutlined />}
        placeholder={t('export.tree.search')}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        style={{ marginBottom: 8 }}
      />
      <Segmented
        block
        size="small"
        value={multi ? 'multi' : 'single'}
        onChange={(v) => setMulti(v === 'multi')}
        options={[
          { value: 'single', label: t('export.tree.single') },
          { value: 'multi', label: t('export.tree.multi') },
        ]}
        style={{ marginBottom: 8 }}
      />
      {!none && (
        <Button type="link" size="small" style={{ padding: 0, marginBottom: 4 }} onClick={() => onChange({ kinds: [], subtypes: [] })}>
          {t('export.tree.clear')}
        </Button>
      )}
      <div className="rp-export-type-tree" role="tree" aria-label={t('export.tree.title')} style={{ maxHeight: 360, overflowY: 'auto' }}>
        {visible.length === 0 && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('export.tree.empty')} />}
        {visible.map((k) => {
          const state = kindCheckState(tree, selection, k.kind)
          const expanded = isOpen(k.kind)
          const kindSelected = selection.kinds.includes(k.kind)
          return (
            <div key={k.kind} role="treeitem" aria-expanded={expanded} aria-selected={multi ? state === 'on' : kindSelected}>
              <div style={rowStyle(!multi && kindSelected)}>
                <Button
                  type="text"
                  size="small"
                  icon={expanded ? <DownOutlined /> : <RightOutlined />}
                  aria-label={t(expanded ? 'export.tree.collapse' : 'export.tree.expand', { name: k.kind })}
                  onClick={() => toggleOpen(k.kind)}
                />
                {multi ? (
                  <Checkbox
                    checked={state === 'on'}
                    indeterminate={state === 'partial'}
                    onChange={() => onChange(toggleKind(tree, selection, k.kind))}
                    style={{ flex: 1, minWidth: 0 }}
                  >
                    <span style={{ fontWeight: 500 }}>{k.kind}</span>
                  </Checkbox>
                ) : (
                  <Button
                    type="text"
                    size="small"
                    style={{ flex: 1, justifyContent: 'flex-start', fontWeight: 500 }}
                    onClick={() => onChange(pickNode(selection, { kind: k.kind }))}
                  >
                    {k.kind}
                  </Button>
                )}
              </div>
              {expanded && (
                <div role="group" style={{ paddingInlineStart: 28 }}>
                  {k.subs.map((s) => {
                    const checked = selection.subtypes.includes(s) || kindSelected
                    return (
                      <div key={s} role="treeitem" aria-selected={checked} style={rowStyle(!multi && selection.subtypes.includes(s))}>
                        {multi ? (
                          <Checkbox checked={checked} onChange={() => onChange(toggleSubtype(tree, selection, s))} style={{ padding: '2px 6px' }}>
                            {s}
                          </Checkbox>
                        ) : (
                          <Button
                            type="text"
                            size="small"
                            block
                            style={{ justifyContent: 'flex-start' }}
                            onClick={() => onChange(pickNode(selection, { subtype: s }))}
                          >
                            {s}
                          </Button>
                        )}
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
