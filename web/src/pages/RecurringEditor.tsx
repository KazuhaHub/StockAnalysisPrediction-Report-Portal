import { useEffect, useMemo, useState } from 'react'
import { App, Button, Card, Input, InputNumber, Select, Space, Switch, TimePicker, Typography } from 'antd'
import { ArrowLeftOutlined, ClockCircleOutlined, SaveOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import { useAuth } from '../auth'
import type { BatchTarget, PluginInput, RecurringDetail, RecurringTask, RecurringTasksResp } from '../api/types'
import { BASE_MAX, visibleOn } from '../lib/batchUi'
import { submittedBatchRows, carryRowsToTarget, type BatchDraftRow } from '../lib/batchRows'
import { notifyError } from '../lib/notify'
import { useLoadFailure } from '../lib/useOnReconnect'
import BatchExecutionFields from '../components/BatchExecutionFields'
import BatchRowsEditor from '../components/BatchRowsEditor'
import BatchTargetPicker from '../components/BatchTargetPicker'
import LoadGate from '../components/LoadGate'
import { usePendingSettings } from './manage/AdminDraftBoundary'

// The recurring-task editor (docs/adr/0018-recurring-tasks.md): a page of its own, composed from the
// same parts as the batch console's "start a batch" card — the workflow picker, the shared rows grid
// and the shared execution fields — with the recurrence and the enabled switch added to the run
// settings. It talks to the same /api/admin/batch/recurring endpoints the old modal did; the firing
// itself stays a trusted backend loop.

const LIST_PATH = '/apps/recurring'
const SHIPPED_MAX_CONCURRENCY = 10

interface Draft {
  name: string
  targetId: number | undefined
  freq: RecurringTask['freq']
  atTime: string
  weekday: number
  monthday: number
  // '' normal | 'idle' | 'urgent' | a base score as a number string.
  priority: string
  concurrency: number
  maxRetries: number
  enabled: boolean
  rows: BatchDraftRow[]
}

const emptyDraft: Draft = {
  name: '',
  targetId: undefined,
  freq: 'daily',
  atTime: '09:30',
  weekday: 1,
  monthday: 1,
  priority: '',
  concurrency: 1,
  maxRetries: 2,
  enabled: true,
  rows: [],
}

// What "has this editor changed?" compares: the blank row the grid keeps at its end is not data.
const snapshot = (draft: Draft) => JSON.stringify({ ...draft, rows: submittedBatchRows(draft.rows) })

export default function RecurringEditor() {
  const { t } = useTranslation()
  const { failure, retryKey } = useLoadFailure({ guardDrafts: true })
  const { message } = App.useApp()
  const { admin } = useAuth()
  const navigate = useNavigate()
  // No :id in the route means a new task.
  const taskId = useParams().id

  const [targets, setTargets] = useState<BatchTarget[]>([])
  // Until the server answers, assume it is no more permissive than its shipped default.
  const [maxConcurrency, setMaxConcurrency] = useState(SHIPPED_MAX_CONCURRENCY)
  const [draft, setDraft] = useState<Draft>(emptyDraft)
  // Columns for the grid when the task's workflow has been deleted (no discovered inputs to fall back
  // on): derived from the stored template so editing an orphaned task never blanks its rows.
  const [fallbackKeys, setFallbackKeys] = useState<string[]>([])
  // The values as opened (blank, or the task as fetched). Dirty is any difference from them, so a
  // failed save stays dirty.
  const [baseline, setBaseline] = useState(() => snapshot(emptyDraft))
  const [editorValid, setEditorValid] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')
  const [saving, setSaving] = useState(false)
  // Set by a successful save. It clears the draft in the same render that starts the trip back to the
  // list, so the draft boundary has nothing left to ask about when the navigation happens.
  const [saved, setSaved] = useState(false)

  const dirty = !saved && snapshot(draft) !== baseline
  usePendingSettings(dirty, saving)
  useEffect(() => {
    if (saved) navigate(LIST_PATH, { replace: true })
  }, [saved, navigate])

  const load = () => {
    setLoading(true)
    setLoadErr('')
    const targetsRequest = api
      .get<{ targets: BatchTarget[] }>('/api/admin/batch/targets')
      .then((r) => visibleOn(r.targets || [], 'recurring'))
    // The list carries the server's own concurrency ceiling. Without it the picker keeps the shipped
    // default, which costs only the upper bound, so a failure here is not worth the whole page.
    api
      .get<RecurringTasksResp>('/api/admin/batch/recurring')
      .then((r) => {
        if (r.maxConcurrency) setMaxConcurrency(r.maxConcurrency)
      })
      .catch(() => {})
    const detailRequest = taskId == null ? Promise.resolve(null) : api.get<RecurringDetail>(`/api/admin/batch/recurring/${encodeURIComponent(taskId)}`)
    let lost = false
    return Promise.all([targetsRequest, detailRequest])
      .then(([visible, d]) => {
        setTargets(visible)
        if (!d) return
        // Columns from the live workflow's inputs, or (workflow deleted) the stored template's own keys.
        const live = visible.find((tg) => tg.id === d.target_id)
        const keys = live?.inputs?.length ? live.inputs.map((i) => i.key) : Object.keys((d.rows || [])[0] || {})
        const opened: Draft = {
          name: d.name,
          targetId: d.target_id,
          freq: d.freq as RecurringTask['freq'],
          atTime: d.at_time || '09:30',
          weekday: d.weekday,
          monthday: d.monthday,
          priority: d.priority || '',
          concurrency: d.concurrency || 1,
          maxRetries: d.max_retries || 0,
          enabled: d.enabled,
          rows: (d.rows || []).map((row) => Object.fromEntries(keys.map((key) => [key, row[key] ?? '']))),
        }
        setFallbackKeys(keys)
        setDraft(opened)
        setBaseline(snapshot(opened))
      })
      .catch((e) => {
        // A lost connection keeps the page loading; it loads again when the connection returns.
        const text = failure(e)
        if (text === null) lost = true
        else setLoadErr(text)
      })
      .finally(() => {
        if (!lost) setLoading(false)
      })
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [retryKey, taskId])

  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => setDraft((d) => ({ ...d, [key]: value }))
  const draftTarget = useMemo(() => targets.find((tg) => tg.id === draft.targetId), [targets, draft.targetId])
  const targetDeleted = draft.targetId != null && !draftTarget
  // Prefer the live workflow's discovered inputs; fall back to the stored template's own columns.
  const inputs = useMemo<PluginInput[]>(
    () => (draftTarget?.inputs?.length ? draftTarget.inputs : fallbackKeys.map((key) => ({ key }))),
    [draftTarget, fallbackKeys],
  )

  // Keep values whose keys exist on the next workflow (the batch console does the same).
  const pickTarget = (id: number | undefined) => {
    const nextInputs = targets.find((tg) => tg.id === id)?.inputs || []
    setFallbackKeys([])
    setEditorValid(false)
    setDraft((d) => ({ ...d, targetId: id, rows: carryRowsToTarget(d.rows, nextInputs) }))
  }

  const save = async () => {
    if (!draft.name.trim()) {
      notifyError(t('recurring.errName'))
      return
    }
    const rows = submittedBatchRows(draft.rows)
    if (!draft.targetId || rows.length === 0) {
      notifyError(t('recurring.errRows'))
      return
    }
    if (!editorValid) {
      notifyError(t('batch.editor.submitBlocked'))
      return
    }
    setSaving(true)
    try {
      const body = {
        name: draft.name.trim(),
        target_id: draft.targetId,
        rows,
        concurrency: draft.concurrency,
        max_retries: draft.maxRetries,
        priority: draft.priority,
        freq: draft.freq,
        at_time: draft.atTime,
        weekday: draft.weekday,
        monthday: draft.monthday,
        enabled: draft.enabled,
      }
      if (taskId == null) await api.post('/api/admin/batch/recurring', body)
      else await api.put(`/api/admin/batch/recurring/${encodeURIComponent(taskId)}`, body)
      message.success(t('common.saved'))
      setSaved(true)
    } catch (e) {
      notifyError(errText(e, t) || t('recurring.errSave'))
    } finally {
      setSaving(false)
    }
  }

  // Leaving goes through the router: the draft boundary above this page asks first when there are
  // unsaved edits, and a clean draft just goes.
  // Replace, not push: the editor is a step out of the list, and the browser's Back must not reopen it.
  const back = () => navigate(LIST_PATH, { replace: true })

  const freqOptions = [
    { value: 'daily', label: t('run.freq.daily') },
    { value: 'weekly', label: t('run.freq.weekly') },
    { value: 'monthly', label: t('run.freq.monthly') },
  ]
  const weekdayOptions = [0, 1, 2, 3, 4, 5, 6].map((d) => ({ value: d, label: t(`run.weekday.${d}`) }))

  return (
    <Space orientation="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <ClockCircleOutlined />
            {taskId == null ? t('recurring.new') : t('recurring.edit')}
          </Space>
        }
        extra={
          <Button icon={<ArrowLeftOutlined />} onClick={back}>
            {t('common.back')}
          </Button>
        }
      >
        <LoadGate loading={loading} error={loadErr} onRetry={load} minHeight={200} title={t('common.loadFailedContent')}>
          {targets.length === 0 && taskId == null ? (
            <Typography.Text type="warning">{t('recurring.noTargets')}</Typography.Text>
          ) : (
            <div className="rp-batch-compose-layout">
              <section className="rp-batch-compose-data">
                <Typography.Title level={5} className="rp-batch-compose-title">
                  {t('batch.editor.dataTitle')}
                </Typography.Title>
                <div className="rp-batch-field-row">
                  <Typography.Text>{t('recurring.fieldName')}</Typography.Text>
                  <Input value={draft.name} onChange={(e) => set('name', e.target.value)} placeholder={t('recurring.namePlaceholder')} />
                </div>
                <BatchTargetPicker
                  targets={targets}
                  value={draft.targetId}
                  onChange={pickTarget}
                  // Keep the deleted workflow's slot labelled so the picker does not show a bare id.
                  missing={targetDeleted ? { id: draft.targetId as number, label: t('recurring.targetGone') } : undefined}
                  warning={targetDeleted ? t('recurring.targetDeletedWarn') : undefined}
                />
                {inputs.length > 0 ? (
                  <BatchRowsEditor
                    key={draft.targetId ?? 'none'}
                    inputs={inputs}
                    rows={draft.rows}
                    onChange={(rows) => set('rows', rows)}
                    onValidityChange={setEditorValid}
                    widthsKey={draftTarget ? `rows-${draftTarget.id}` : undefined}
                  />
                ) : (
                  <div className="rp-batch-editor-empty">
                    <Typography.Text type="secondary">{t('batch.selectTargetFirst')}</Typography.Text>
                  </div>
                )}
              </section>

              <aside className="rp-batch-compose-settings">
                <Typography.Title level={5} className="rp-batch-compose-title">
                  {t('run.settings')}
                </Typography.Title>

                <div className="rp-recurring-cadence">
                  <Typography.Text>{t('recurring.fieldCadence')}</Typography.Text>
                  <div className="rp-recurring-cadence__controls">
                    <Select
                      style={{ width: 120 }}
                      value={draft.freq}
                      onChange={(v) => set('freq', v)}
                      options={freqOptions}
                      aria-label={t('recurring.fieldCadence')}
                    />
                    <TimePicker
                      format="HH:mm"
                      allowClear={false}
                      needConfirm={false}
                      value={dayjs('2000-01-01 ' + draft.atTime)}
                      onChange={(d) => set('atTime', d ? d.format('HH:mm') : '09:30')}
                      aria-label={t('recurring.fieldCadence')}
                    />
                    {draft.freq === 'weekly' && (
                      <Select style={{ width: 120 }} value={draft.weekday} onChange={(v) => set('weekday', v)} options={weekdayOptions} />
                    )}
                    {draft.freq === 'monthly' && (
                      <Space size={4}>
                        <Typography.Text type="secondary">{t('recurring.dayOfMonth')}</Typography.Text>
                        <InputNumber min={1} max={31} value={draft.monthday} onChange={(v) => set('monthday', v ?? 1)} />
                      </Space>
                    )}
                  </div>
                  <Typography.Text type="secondary" className="rp-recurring-cadence__hint">
                    {t('recurring.tzHint')}
                  </Typography.Text>
                </div>

                <BatchExecutionFields
                  maxRetries={draft.maxRetries}
                  onMaxRetries={(v) => set('maxRetries', v)}
                  rowConcurrency={draft.concurrency}
                  onRowConcurrency={(v) => set('concurrency', v)}
                  maxConcurrency={maxConcurrency}
                >
                  <PriorityField priority={draft.priority} admin={admin} onChange={(v) => set('priority', v)} />
                </BatchExecutionFields>

                <div className="rp-batch-setting-fields">
                  <label>
                    <Typography.Text>{t('recurring.fieldEnabled')}</Typography.Text>
                    <Switch checked={draft.enabled} onChange={(v) => set('enabled', v)} />
                  </label>
                </div>

                <Button block type="primary" size="large" icon={<SaveOutlined />} loading={saving} onClick={save}>
                  {t('common.save')}
                </Button>
              </aside>
            </div>
          )}
        </LoadGate>
      </Card>
    </Space>
  )
}

// Where the batch console puts its priority number, a recurring task puts its priority mode. The
// Select shows a "mode"; the stored value is '' | 'idle' | 'urgent' | a number string. Normal and
// idle are open to anyone; admins also get urgent (top priority) and a custom base score.
function PriorityField({ priority, admin, onChange }: { priority: string; admin: boolean; onChange: (value: string) => void }) {
  const { t } = useTranslation()
  const mode = priority === '' ? 'normal' : priority === 'idle' ? 'idle' : priority === 'urgent' ? 'urgent' : 'custom'
  const setMode = (next: string) => onChange(next === 'normal' ? '' : next === 'custom' ? '80' : next)
  return (
    <>
      <label>
        <Typography.Text>{t('batch.priorityLabel')}</Typography.Text>
        <Select
          value={mode}
          onChange={setMode}
          aria-label={t('batch.priorityLabel')}
          options={[
            { value: 'normal', label: t('recurring.priorityNormal') },
            { value: 'idle', label: t('recurring.priorityIdleLong') },
            ...(admin
              ? [
                  { value: 'urgent', label: t('recurring.priorityUrgent') },
                  { value: 'custom', label: t('recurring.priorityCustom') },
                ]
              : []),
          ]}
        />
      </label>
      {admin && mode === 'custom' && (
        <label>
          <Typography.Text>{t('recurring.priorityCustom')}</Typography.Text>
          <InputNumber
            min={0}
            max={BASE_MAX}
            value={Number(priority) || 0}
            onChange={(v) => onChange(String(v ?? 0))}
            aria-label={t('recurring.priorityCustom')}
          />
        </label>
      )}
    </>
  )
}
