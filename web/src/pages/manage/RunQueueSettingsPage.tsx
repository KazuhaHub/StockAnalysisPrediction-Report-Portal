import { useEffect, useState } from 'react'
import { App, ConfigProvider, Input, InputNumber, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { api, errText } from '../../api/client'
import type { BatchConfig } from '../../api/types'
import RunPresetsEditor from './RunPresetsEditor'
import LoadGate from '../../components/LoadGate'
import SettingsWorkspace, { SettingsPanel } from './SettingsWorkspace'
import SettingsSaveBar from './SettingsSaveBar'
import SettingsField from './SettingsField'
import type { ReactElement } from 'react'
import CompactNumberInput from '../../components/CompactNumberInput'
import { PolicyPage } from './OUPolicyWorkspace'

// Standalone run-queue settings (docs/adr/0007 + 0008): the queue concurrency budget,
// the default base priority, the Dify end-user template, and the Slurm-style multifactor
// priority weights. These govern the whole run system (single run + CSV batch). The
// global urgent lane and ticket settings are part of queue capacity. Per-OU overrides use the
// organizational scope; run-form defaults retain their own page.
export default function RunQueueSettingsPage() {
  return <PolicyPage section="runs"><GlobalRunQueueSettings /></PolicyPage>
}

function GlobalRunQueueSettings() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [maxJobs, setMaxJobs] = useState(1)
  // The per-job worker ceiling (ADR 0001). It has always been read and clamped against; until now
  // nothing could write it, so it sat at its compiled-in default while the run forms offered double.
  const [maxConcurrency, setMaxConcurrency] = useState(10)
  const [difyEndUser, setDifyEndUser] = useState('')
  const [difyPollSeconds, setDifyPollSeconds] = useState(0)
  const [difyRunTimeout, setDifyRunTimeout] = useState(180)
  const [defaultPriority, setDefaultPriority] = useState(50)
  const [wBase, setWBase] = useState(1000)
  const [wAge, setWAge] = useState(1000)
  const [wFair, setWFair] = useState(1000)
  const [ageHours, setAgeHours] = useState(24)
  const [fairHalflife, setFairHalflife] = useState(168)
  const [urgentEnabled, setUrgentEnabled] = useState(false)
  const [reservedSlots, setReservedSlots] = useState(1)
  const [ticketPeriod, setTicketPeriod] = useState(7)
  const values = { maxJobs, maxConcurrency, difyEndUser, difyPollSeconds, difyRunTimeout, defaultPriority, wBase, wAge, wFair, ageHours, fairHalflife, urgentEnabled, reservedSlots, ticketPeriod }
  const [saved, setSaved] = useState<typeof values | null>(null)
  const [saving, setSaving] = useState(false)
  const [section, setSection] = useState('capacity')
  const dirty = saved !== null && JSON.stringify(values) !== JSON.stringify(saved)
  const restore = (v: typeof values) => {
    setMaxJobs(v.maxJobs); setMaxConcurrency(v.maxConcurrency); setDifyEndUser(v.difyEndUser)
    setDifyPollSeconds(v.difyPollSeconds); setDifyRunTimeout(v.difyRunTimeout); setDefaultPriority(v.defaultPriority)
    setWBase(v.wBase); setWAge(v.wAge); setWFair(v.wFair); setAgeHours(v.ageHours); setFairHalflife(v.fairHalflife)
    setUrgentEnabled(v.urgentEnabled); setReservedSlots(v.reservedSlots); setTicketPeriod(v.ticketPeriod)
  }
  // Every field above starts at a plausible-looking number, and this form is live. Until the
  // server has answered, those numbers are this file's opinion, not the queue's configuration —
  // so nothing renders behind the gate, and a failed load says so instead of quietly offering
  // an admin the chance to save 1-job-at-a-time over whatever is really set.
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return api
      .get<BatchConfig>('/api/admin/batch/config')
      .then((r) => {
        const next = { maxJobs: r.max_jobs, maxConcurrency: r.max_concurrency || 10, difyEndUser: r.dify_end_user ?? '',
          difyPollSeconds: r.dify_poll_seconds ?? 0, difyRunTimeout: r.dify_run_timeout_minutes ?? 180,
          defaultPriority: r.default_priority ?? 50, wBase: r.prio_w_base, wAge: r.prio_w_age,
          wFair: r.prio_w_fair, ageHours: r.prio_age_hours, fairHalflife: r.prio_fair_halflife_hours,
          urgentEnabled: !!r.urgent_enabled, reservedSlots: r.reserved_slots ?? 1, ticketPeriod: r.ticket_period_days ?? 7 }
        restore(next); setSaved(next)
      })
      .catch((e) => setLoadErr(errText(e, t)))
      .finally(() => setLoading(false))
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const save = async () => {
    setSaving(true)
    try {
    await api.post('/api/admin/batch/config', {
      max_jobs: maxJobs,
      max_concurrency: maxConcurrency,
      dify_end_user: difyEndUser,
      dify_poll_seconds: difyPollSeconds,
      dify_run_timeout_minutes: difyRunTimeout,
      default_priority: String(defaultPriority),
      prio_w_base: wBase,
      prio_w_age: wAge,
      prio_w_fair: wFair,
      prio_age_hours: ageHours,
      prio_fair_halflife_hours: fairHalflife,
      urgent_enabled: urgentEnabled,
      reserved_slots: reservedSlots,
      ticket_period_days: ticketPeriod,
    })
    message.success(t('common.saved'))
    setSaved(values)
    } catch (e) { message.error(errText(e, t)) } finally { setSaving(false) }
  }

  const row = (label: string, hint: string, control: ReactElement) => <SettingsField label={label} hint={hint}>{control}</SettingsField>

  return (
    <>
      <LoadGate loading={loading} error={loadErr} onRetry={load}>
      <SettingsWorkspace active={section} onChange={setSection} sections={[
        { key: 'capacity', label: t('manage.queue.capacity') }, { key: 'dify', label: t('batch.admin.difyTitle') },
        { key: 'priority', label: t('batch.admin.prioWeightsTitle') }, { key: 'presets', label: t('batch.admin.presetsTitle') },
        { key: 'urgent', label: t('users.urgentTitle') },
      ]}>
      <ConfigProvider componentDisabled={saving}>
      <SettingsPanel section="capacity">
        {row(
          t('batch.admin.maxJobs'),
          t('batch.admin.maxJobsHint'),
          <InputNumber min={1} max={50} value={maxJobs} onChange={(v) => setMaxJobs(v || 1)} />,
        )}
        {row(
          t('batch.admin.maxConcurrency'),
          t('batch.admin.maxConcurrencyHint'),
          <InputNumber min={1} max={100} value={maxConcurrency} onChange={(v) => setMaxConcurrency(v || 1)} />,
        )}
        {row(
          t('batch.admin.defaultPriority'),
          t('batch.admin.defaultPriorityHint'),
          <InputNumber min={0} max={100} value={defaultPriority} onChange={(v) => setDefaultPriority(v ?? 50)} />,
        )}

      </SettingsPanel>
      <SettingsPanel section="urgent">
        {row(t('batch.admin.urgentEnabled'), t('batch.admin.urgentEnabledHint'), <Switch checked={urgentEnabled} onChange={setUrgentEnabled} />)}
        {urgentEnabled && <>
          {row(t('batch.admin.reservedSlots'), t('batch.admin.reservedSlotsHint'), <InputNumber min={0} max={Math.max(0, maxJobs - 1)} value={reservedSlots} onChange={(v) => setReservedSlots(v ?? 0)} />)}
          {row(t('batch.admin.ticketPeriod'), t('batch.admin.ticketPeriodHint'), <CompactNumberInput min={1} max={365} value={ticketPeriod} onChange={(v) => setTicketPeriod(v ?? 7)} after={t('batch.admin.days')} />)}
        </>}
      </SettingsPanel>
      <SettingsPanel section="dify">
                  {row(
                    t('batch.admin.difyEndUser'),
                    t('batch.admin.difyEndUserHint'),
                    <Input value={difyEndUser} placeholder="report-portal" onChange={(e) => setDifyEndUser(e.target.value)} />,
                  )}
                  {row(
                    t('batch.admin.difyPoll'),
                    t('batch.admin.difyPollHint'),
                    <CompactNumberInput min={0} max={600} value={difyPollSeconds} onChange={(v) => setDifyPollSeconds(v ?? 0)} after={t('batch.admin.seconds')} />,
                  )}
                  {row(
                    t('batch.admin.difyRunTimeout'),
                    t('batch.admin.difyRunTimeoutHint'),
                    <CompactNumberInput min={1} max={720} value={difyRunTimeout} onChange={(v) => setDifyRunTimeout(v || 180)} after={t('batch.admin.minutes')} />,
                  )}
      </SettingsPanel>
      <SettingsPanel section="priority">
                  <Typography.Text type="secondary">{t('batch.admin.prioWeightsHint')}</Typography.Text>
                  {row(
                    t('batch.admin.wBase'),
                    t('batch.admin.wBaseHint'),
                    <InputNumber min={0} step={100} value={wBase} onChange={(v) => setWBase(v ?? 0)} />,
                  )}
                  {row(
                    t('batch.admin.wAge'),
                    t('batch.admin.wAgeHint'),
                    <InputNumber min={0} step={100} value={wAge} onChange={(v) => setWAge(v ?? 0)} />,
                  )}
                  {row(
                    t('batch.admin.wFair'),
                    t('batch.admin.wFairHint'),
                    <InputNumber min={0} step={100} value={wFair} onChange={(v) => setWFair(v ?? 0)} />,
                  )}
                  {row(
                    t('batch.admin.ageHours'),
                    t('batch.admin.ageHoursHint'),
                    <CompactNumberInput min={1} max={8760} value={ageHours} onChange={(v) => setAgeHours(v || 24)} after={t('batch.admin.hours')} />,
                  )}
                  {row(
                    t('batch.admin.fairHalflife'),
                    t('batch.admin.fairHalflifeHint'),
                    <CompactNumberInput min={1} max={8760} value={fairHalflife} onChange={(v) => setFairHalflife(v || 168)} after={t('batch.admin.hours')} />,
                  )}
      </SettingsPanel>
      </ConfigProvider>
      <SettingsPanel section="presets">
        <Typography.Paragraph type="secondary">{t('batch.admin.presetsHint')}</Typography.Paragraph>
        <RunPresetsEditor />
        {/* Which window (if any) the run form pre-picks is a run-form default, not a queue setting;
            it lives with the rest of them one page over. */}
        <Typography.Text type="secondary">
          {t('batch.admin.runDefaultsMoved')} <Link to="/manage/rundefaults">{t('nav.runDefaults')}</Link>
        </Typography.Text>
      </SettingsPanel>
      </SettingsWorkspace>
      {(section !== 'presets' || dirty) && <SettingsSaveBar scope={t('manage.label.runqueue')} dirty={dirty} saving={saving} onSave={save} onDiscard={() => { if (saved) restore(saved) }} />}
      </LoadGate>
    </>
  )
}
