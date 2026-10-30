import { useEffect, useMemo, useState } from 'react'
import {
  Alert,
  App,
  Button,
  DatePicker,
  Drawer,
  Empty,
  Form,
  Input,
  Popconfirm,
  Radio,
  Segmented,
  Select,
  Space,
  Switch,
  Tag,
  Tooltip,
  Typography,
  theme,
} from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import useFormDraft from './useFormDraft'
import dayjs from 'dayjs'
import { api, errText } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import { settleOnAnswer, type LoadOpts } from '../../lib/loadFailure'
import { forgetTags } from '../../lib/conditionalGet'
import { useAnnouncements } from '../../announcements'
import type {
  AdminAnnouncement,
  Announcement,
  AnnouncementAudience,
  AnnouncementDisplay,
  AnnouncementLevel,
  AnnouncementScope,
} from '../../api/types'
import { AnnouncementRow } from '../../components/SiteAnnouncement'
import type { Principal } from '../../api/types'
import { DragHandle, SortableItem, SortableWrapper } from './dnd'
import { principalName, principalOptions } from '../../components/principals'
import LoadGate from '../../components/LoadGate'

const { RangePicker } = DatePicker
const ANNOUNCEMENT_LEVELS: AnnouncementLevel[] = ['notice', 'success', 'warning', 'error']

const LEVEL_DOT: Record<AnnouncementLevel, string> = {
  notice: 'colorInfo',
  success: 'colorSuccess',
  warning: 'colorWarning',
  error: 'colorError',
}

// An enabled announcement with no end time that nobody has touched in this long gets an amber
// nudge in the list. It is the cheap 90% of the display window, for the operator who will not set
// one: an incident banner nobody remembers to take down is how a whole announcement band stops
// being read.
const STALE_DAYS = 14

// Above this many announcements live at once, the console says so. Not a limit — saving is never
// refused, because a refusal at the moment somebody most needs to broadcast does not actually stop
// readers from ignoring an overcrowded band. It counts what readers face (enabled and inside its
// window), not the draft pile, because that is the number that degrades the band.
const CROWDED = 5

interface FormValues {
  enabled: boolean
  stripDismissible: boolean
  popupDismissible: boolean
  level: AnnouncementLevel
  scope: AnnouncementScope
  audience: AnnouncementAudience
  display: AnnouncementDisplay
  grants: string[]
  title: string
  content: string
  window?: [dayjs.Dayjs | null, dayjs.Dayjs | null] | null
}

// Site announcements. One row per announcement, dragged into the order readers see, each saved on
// its own — there is no page-level Save button, the way the entry-buttons page works.
export default function AnnouncementPage() {
  const { t } = useTranslation()
  const { catchLoad, retryKey } = useLoadFailure()
  const { message } = App.useApp()
  const { refresh: refreshFeed } = useAnnouncements()
  const { token } = theme.useToken()
  const [items, setItems] = useState<AdminAnnouncement[]>([])
  const [groups, setGroups] = useState<Principal[]>([])
  const [users, setUsers] = useState<Principal[]>([])
  const [usersTruncated, setUsersTruncated] = useState(false)
  const [collapse, setCollapse] = useState(false)
  const [previewAs, setPreviewAs] = useState<string | undefined>()
  const [preview, setPreview] = useState<Announcement[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [editing, setEditing] = useState<AdminAnnouncement | null>(null)
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [form] = Form.useForm<FormValues>()
  const draft = useFormDraft(form, open, saving, () => setOpen(false))

  const load = (opts?: LoadOpts) => {
    setLoading(true)
    setLoadErr('')
    return api
      .get<{
        items: AdminAnnouncement[]
        groups: Principal[]
        users: Principal[]
        usersTruncated: boolean
        collapse: boolean
      }>('/api/admin/announcements')
      .then((r) => {
        setItems(r.items || [])
        setCollapse(r.collapse === true)
        setGroups(r.groups || [])
        setUsers(r.users || [])
        setUsersTruncated(r.usersTruncated === true)
        setLoaded(true)
      })
      // Not a `finally`-only settle: a failed GET that renders as "no announcements yet" looks
      // exactly like a portal that has none, and the drag handler would then be able to send
      // "replace the whole order" from a list that was never there.
      .then(...settleOnAnswer(() => setLoading(false), opts))
      .catch(catchLoad(setLoadErr, opts))
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [retryKey])

  // The admin's own shell polls the reader feed every 60s. After a change, drop its stored ETag AND
  // ask again, so the edit shows up in this tab now rather than up to a minute later. Dropping the
  // tag alone only made the next poll a full one: an admin who switched "readers can close it" off
  // and went straight back to check still found the close button, from the payload before the edit.
  const feedChanged = () => {
    forgetTags('/api/announcements')
    void refreshFeed()
  }
  const refreshed = () => {
    feedChanged()
    return load({ quiet: true })
  }

  const ids = items.map((a) => String(a.id))

  const reorder = (orderedIds: string[]) => {
    const byId = new Map(items.map((a) => [String(a.id), a]))
    const next = orderedIds.map((k, i) => ({ ...(byId.get(k) as AdminAnnouncement), ord: i }))
    setItems(next) // optimistic; the server is the arbiter and a rejection reloads
    api
      .post('/api/admin/announcements/reorder', { ids: orderedIds.map(Number) })
      .then(feedChanged)
      // A 409 means somebody else changed the list while this page held an old copy. Reloading is
      // the honest response — better a list that visibly refreshes than an order that silently
      // reverts on the next visit.
      .catch((e) => {
        message.error(errText(e, t))
        load({ quiet: true })
      })
  }

  const openEdit = (a: AdminAnnouncement | null) => {
    setEditing(a)
    form.setFieldsValue({
      enabled: a ? a.enabled : true,
      display: a ? a.display : 'strip',
      // A new announcement locks the band (an operator opts in to a close button) and leaves the
      // popup dismissible, the two defaults the server applies as well.
      stripDismissible: a ? a.stripDismissible : false,
      popupDismissible: a ? a.popupDismissible : true,
      level: a?.level || 'notice',
      scope: a?.scope || 'home',
      audience: a?.audience || 'all',
      grants: a?.grants || [],
      title: a?.title || '',
      content: a?.content || '',
      window: a && (a.startsAt || a.endsAt) ? [a.startsAt ? dayjs(a.startsAt) : null, a.endsAt ? dayjs(a.endsAt) : null] : null,
    })
    setOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    const [from, to] = v.window ?? [null, null]
    const payload = {
      enabled: v.enabled === true,
      display: v.display || 'strip',
      stripDismissible: v.stripDismissible === true,
      popupDismissible: v.popupDismissible === true,
      level: v.level || 'notice',
      scope: v.scope || 'home',
      audience: v.audience || 'all',
      // Always sent from this form, which holds the whole row. The field is optional on the wire
      // precisely so a partial writer can leave the audience alone; this is not one.
      grants: v.audience === 'grant' ? v.grants || [] : [],
      title: v.title || '',
      content: v.content || '',
      startsAt: from ? from.toISOString() : '',
      endsAt: to ? to.toISOString() : '',
      // Sent so the server can refuse a save built on a copy somebody else has already replaced.
      ...(editing ? { updatedAt: editing.updatedAt } : {}),
    }
    setSaving(true)
    try {
      if (editing) await api.put(`/api/admin/announcements/${editing.id}`, payload)
      else await api.post('/api/admin/announcements', payload)
      setOpen(false)
      message.success(t('common.saved'))
      await refreshed()
    } finally {
      setSaving(false)
    }
  }

  // The inline controls PATCH one field. Re-sending the whole row to flip one would write back the
  // title and body this page loaded, silently reverting another operator's edit.
  const toggle = async (a: AdminAnnouncement, patch: { enabled?: boolean; display?: AnnouncementDisplay }) => {
    setItems((prev) => prev.map((x) => (x.id === a.id ? { ...x, ...patch } : x)))
    try {
      await api.patch(`/api/admin/announcements/${a.id}`, patch)
      feedChanged()
    } catch (e) {
      message.error(errText(e, t))
      load({ quiet: true })
    }
  }

  // One site-wide display switch, saved on its own the moment it is flipped — the settings endpoint
  // merges per field, so posting this alone cannot disturb the branding beside it.
  const saveCollapse = async (on: boolean) => {
    setCollapse(on) // optimistic; a failure puts it back
    try {
      await api.post('/api/admin/settings', { announcementCollapse: on })
      feedChanged()
    } catch (e) {
      setCollapse(!on)
      message.error(errText(e, t))
    }
  }

  const remove = async (id: number) => {
    await api.del(`/api/admin/announcements/${id}`)
    await refreshed()
  }

  // Two groups, the way the report-versions picker does it, so an admin who has used one knows
  // this one. Shared with the report editor, which addresses the same principals.
  const audienceOptions = principalOptions(groups, users, {
    groups: t('announcementAdmin.principalGroups'),
    users: t('announcementAdmin.principalUsers'),
    restricted: t('announcementAdmin.restrictedTag'),
  })
  const nameOf = (p: string) => principalName(groups, users, p)

  const loadPreview = (principal?: string) => {
    setPreviewAs(principal)
    setPreview(null)
    if (!principal) return
    api
      .get<{ items: Announcement[] }>(`/api/admin/announcements/preview?principal=${encodeURIComponent(principal)}`)
      .then((r) => setPreview(r.items || []))
      .catch((e) => {
        message.error(errText(e, t))
        setPreviewAs(undefined)
      })
  }

  // A targeted announcement with no recipients left reaches nobody and says nothing about it. The
  // save path refuses to create one, but deleting the group it named produces the same state after
  // the fact — so it has to be a status the list reports, not only a validation rule.
  const unreachable = (a: AdminAnnouncement) => a.audience === 'grant' && a.grants.length === 0

  const statusOf = (a: AdminAnnouncement) => {
    if (!a.enabled) return { key: 'draft', color: undefined }
    if (unreachable(a)) return { key: 'unreachable', color: 'red' }
    const now = Date.now()
    if (a.startsAt && Date.parse(a.startsAt) > now) return { key: 'scheduled', color: 'blue' }
    if (a.endsAt && Date.parse(a.endsAt) <= now) return { key: 'expired', color: undefined }
    return { key: 'live', color: 'green' }
  }

  const liveCount = items.filter((a) => statusOf(a).key === 'live').length

  const staleDays = (a: AdminAnnouncement) => {
    if (!a.enabled || a.endsAt || !a.updatedAt) return 0
    const days = Math.floor((Date.now() - Date.parse(a.updatedAt)) / 86400000)
    return Number.isNaN(days) || days < STALE_DAYS ? 0 : days
  }

  const row = (a: AdminAnnouncement) => {
    const status = statusOf(a)
    const stale = staleDays(a)
    const preview = (a.content || '').split('\n')[0]
    return (
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          flexWrap: 'wrap',
          padding: '8px 10px',
          borderRadius: 8,
          background: token.colorFillQuaternary,
          border: `1px solid ${token.colorBorderSecondary}`,
          opacity: a.enabled ? 1 : 0.55, // a switched-off row reads as off at a glance
        }}
      >
        <DragHandle label={t('common.reorder')} />
        <span
          aria-hidden
          style={{
            width: 8,
            height: 8,
            borderRadius: 4,
            flexShrink: 0,
            background: (token as unknown as Record<string, string>)[LEVEL_DOT[a.level]],
          }}
        />
        <Typography.Text strong={!!a.title} type={a.title ? undefined : 'secondary'}>
          {a.title || t('announcementAdmin.untitled')}
        </Typography.Text>
        <Typography.Text type="secondary" ellipsis style={{ flex: 1, minWidth: 80, fontSize: 12 }}>
          {preview}
        </Typography.Text>
        {status.key === 'unreachable' ? (
          <Tooltip title={t('announcementAdmin.unreachableHint')}>
            <Tag color="red">{t('announcementAdmin.status.unreachable')}</Tag>
          </Tooltip>
        ) : (
          <Tag color={status.color}>{t(`announcementAdmin.status.${status.key}`)}</Tag>
        )}
        {a.scope === 'app' && <Tag color="geekblue">{t('announcementAdmin.scope.app')}</Tag>}
        {a.audience === 'grant' && (
          <Tooltip title={a.grants.map(nameOf).join('、')}>
            <Tag color="cyan">
              {a.grants.length === 1
                ? nameOf(a.grants[0])
                : t('announcementAdmin.audienceCount', { count: a.grants.length })}
            </Tag>
          </Tooltip>
        )}
        {a.display !== 'strip' && <Tag color="purple">{t('announcementAdmin.popupTag')}</Tag>}
        {stale > 0 && (
          <Tooltip title={t('announcementAdmin.staleHint')}>
            <Tag color="orange">{t('announcementAdmin.stale', { count: stale })}</Tag>
          </Tooltip>
        )}
        <Tooltip title={t('announcementAdmin.enabled')}>
          <Switch
            size="small"
            aria-label={t('announcementAdmin.enabled')}
            checked={a.enabled}
            onChange={(v) => toggle(a, { enabled: v })}
          />
        </Tooltip>
        <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(a)} aria-label={t('common.edit')} />
        <Popconfirm title={t('announcementAdmin.deleteConfirm')} onConfirm={() => remove(a.id)}>
          <Button size="small" danger icon={<DeleteOutlined />} aria-label={t('common.delete')} />
        </Popconfirm>
      </div>
    )
  }

  // The editor's live rendering of the row being edited. Named apart from `preview`, which is the
  // "what would this OU see" panel above the list — two different questions.
  const livePreview = useMemo(
    () => (
      <Form.Item shouldUpdate noStyle>
        {({ getFieldValue }) => {
          const title = String(getFieldValue('title') || '').trim()
          const content = String(getFieldValue('content') || '').trim()
          if (!title && !content) return null
          return (
            <AnnouncementRow
              announcement={{
                level: getFieldValue('level') || 'notice',
                title,
                content,
                stripDismissible: getFieldValue('stripDismissible') === true,
              }}
              style={{ marginTop: 4 }}
            />
          )
        }}
      </Form.Item>
    ),
    [],
  )

  return (
    <Space orientation="vertical" size={12} style={{ width: '100%', minWidth: 0 }}>
      <LoadGate loading={loading} error={loadErr} onRetry={load}>
        <Space orientation="vertical" size={10} style={{ width: '100%' }}>
          <Space wrap>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => openEdit(null)}>
              {t('announcementAdmin.add')}
            </Button>
            {/* You do not receive your own targeted announcements — the audience filter applies to
                admins too — so "addressed correctly" and "addressed to nobody" look identical from
                here. This is the only cheap way to tell them apart before publishing, and it asks
                the server the same question a real reader's browser asks. */}
            <Select
              allowClear
              showSearch
              optionFilterProp="label"
              style={{ minWidth: 200 }}
              placeholder={t('announcementAdmin.previewAs')}
              value={previewAs}
              onChange={loadPreview}
              options={audienceOptions}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('announcementAdmin.hint')}
            </Typography.Text>
          </Space>

          <Space size={8} align="center" wrap>
            <Switch
              size="small"
              aria-label={t('announcementAdmin.collapse')}
              checked={collapse}
              onChange={saveCollapse}
            />
            <Typography.Text style={{ fontSize: 13 }}>{t('announcementAdmin.collapse')}</Typography.Text>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('announcementAdmin.collapseHint')}
            </Typography.Text>
          </Space>

          {previewAs && (
            <div
              style={{
                border: `1px dashed ${token.colorBorder}`,
                borderRadius: 8,
                padding: 12,
                background: token.colorFillQuaternary,
              }}
            >
              <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 8 }}>
                {t('announcementAdmin.previewFor', { name: nameOf(previewAs) })}
              </Typography.Text>
              {preview === null ? (
                <Typography.Text type="secondary">{t('common.loading')}</Typography.Text>
              ) : preview.length === 0 ? (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('announcementAdmin.previewEmpty')} />
              ) : (
                <Space orientation="vertical" size={8} style={{ width: '100%' }}>
                  {preview.map((a) => (
                    <AnnouncementRow key={a.id} announcement={a} />
                  ))}
                </Space>
              )}
            </div>
          )}

          {liveCount > CROWDED && (
            <Alert
              showIcon
              type="warning"
              title={t('announcementAdmin.crowded', { count: liveCount })}
              style={{ borderRadius: 8 }}
            />
          )}

          {items.length === 0 ? (
            <Empty description={t('announcementAdmin.empty')} />
          ) : (
            // Dragging is disabled until the list has actually loaded: reorder replaces the whole
            // order in one call, and a page that never rendered has no order worth sending.
            <SortableWrapper ids={loaded ? ids : []} onReorder={reorder} gap={8}>
              {items.map((a) => (
                <SortableItem key={a.id} id={String(a.id)}>
                  {row(a)}
                </SortableItem>
              ))}
            </SortableWrapper>
          )}
        </Space>
      </LoadGate>

      <Drawer
        open={open}
        onClose={draft.close}
        closable={!saving}
        maskClosable={!saving}
        size={520}
        destroyOnHidden
        title={editing ? t('announcementAdmin.editTitle') : t('announcementAdmin.add')}
        footer={
          <Space style={{ width: '100%', justifyContent: 'flex-end' }}>
            <Button disabled={saving} onClick={draft.close}>{t('common.cancel')}</Button>
            <Button type="primary" loading={saving} onClick={submit}>
              {t('common.save')}
            </Button>
          </Space>
        }
      >
        <Form form={form} disabled={saving} layout="vertical">
          <Form.Item name="enabled" label={t('announcementAdmin.enabled')} valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name="display" label={t('announcementAdmin.display')} extra={t('announcementAdmin.displayHint')}>
            <Segmented
              options={[
                { value: 'strip', label: t('announcementAdmin.display.strip') },
                { value: 'popup', label: t('announcementAdmin.display.popup') },
                { value: 'both', label: t('announcementAdmin.display.both') },
              ]}
            />
          </Form.Item>
          {/* One switch per surface, shown only for the surface this announcement is drawn on. The
              other is hidden, not unmounted: it stays registered in the form, so a save sends every
              value and never drops one just because its switch is off screen. */}
          <Form.Item noStyle shouldUpdate={(prev, next) => prev.display !== next.display}>
            {({ getFieldValue }) => {
              const display = getFieldValue('display') || 'strip'
              return (
                <div style={{ marginBottom: 24 }}>
                  <div style={{ marginBottom: 8 }}>{t('announcementAdmin.dismissible')}</div>
                  <Form.Item
                    name="stripDismissible"
                    label={t('announcementAdmin.stripDismissible')}
                    extra={t('announcementAdmin.stripDismissibleHint')}
                    valuePropName="checked"
                    hidden={display === 'popup'}
                    style={{ marginBottom: 12 }}
                  >
                    <Switch />
                  </Form.Item>
                  <Form.Item
                    name="popupDismissible"
                    label={t('announcementAdmin.popupDismissible')}
                    extra={t('announcementAdmin.popupDismissibleHint')}
                    valuePropName="checked"
                    hidden={display === 'strip'}
                    style={{ marginBottom: 0 }}
                  >
                    <Switch />
                  </Form.Item>
                </div>
              )
            }}
          </Form.Item>
          <Form.Item name="level" label={t('settings.announcementLevel')}>
            <Select
              options={ANNOUNCEMENT_LEVELS.map((level) => ({
                value: level,
                label: (
                  <Space size={8}>
                    <span
                      aria-hidden
                      style={{
                        display: 'inline-block',
                        width: 8,
                        height: 8,
                        borderRadius: 4,
                        background: (token as unknown as Record<string, string>)[LEVEL_DOT[level]],
                      }}
                    />
                    {t(`settings.announcementLevel.${level}`)}
                  </Space>
                ),
              }))}
            />
          </Form.Item>
          <Form.Item name="scope" label={t('announcementAdmin.scope')} extra={t('announcementAdmin.scopeHint')}>
            <Segmented
              options={[
                { value: 'home', label: t('announcementAdmin.scope.home') },
                { value: 'app', label: t('announcementAdmin.scope.app') },
              ]}
            />
          </Form.Item>
          <Form.Item name="audience" label={t('announcementAdmin.audience')}>
            <Radio.Group
              options={[
                { value: 'all', label: t('announcementAdmin.audience.all') },
                { value: 'grant', label: t('announcementAdmin.audience.grant') },
              ]}
              optionType="button"
            />
          </Form.Item>
          <Form.Item shouldUpdate={(a, b) => a.audience !== b.audience} noStyle>
            {({ getFieldValue }) =>
              getFieldValue('audience') === 'grant' ? (
                <Form.Item
                  name="grants"
                  label={t('announcementAdmin.grants')}
                  extra={usersTruncated ? t('announcementAdmin.grantsTruncated', { count: users.length }) : undefined}
                  // Saving a targeted announcement with nobody in its audience stores a row that
                  // reaches no one and reports no problem — the standard path to an operator
                  // concluding the feature is broken. The server refuses it too.
                  rules={[{ required: true, message: t('announcementAdmin.grantsRequired') }]}
                >
                  <Select
                    mode="multiple"
                    allowClear
                    options={audienceOptions}
                    optionFilterProp="label"
                    placeholder={t('announcementAdmin.grantsPlaceholder')}
                  />
                </Form.Item>
              ) : null
            }
          </Form.Item>
          <Form.Item
            name="title"
            label={t('settings.announcementTitle')}
            rules={[{ max: 160, message: t('settings.announcementTitleTooLong') }]}
          >
            <Input maxLength={160} showCount placeholder={t('settings.announcementTitlePlaceholder')} />
          </Form.Item>
          <Form.Item
            name="content"
            label={t('settings.announcementContent')}
            rules={[{ max: 2000, message: t('settings.announcementContentTooLong') }]}
          >
            <Input.TextArea
              maxLength={2000}
              showCount
              autoSize={{ minRows: 5, maxRows: 14 }}
              placeholder={t('settings.announcementContentPlaceholder')}
            />
          </Form.Item>
          <Form.Item name="window" label={t('announcementAdmin.window')} extra={t('announcementAdmin.windowHint')}>
            <RangePicker showTime style={{ width: '100%' }} allowEmpty={[true, true]} />
          </Form.Item>
          {livePreview}
        </Form>
      </Drawer>
    </Space>
  )
}
