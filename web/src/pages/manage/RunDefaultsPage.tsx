import { cloneElement, useEffect, useMemo, useState } from 'react'
import { App, ConfigProvider, InputNumber, Select, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { api, errText } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import { settleOnAnswer } from '../../lib/loadFailure'
import { visibleOn } from '../../lib/batchUi'
import { presetSummary } from '../../lib/runSchedule'
import type { BatchConfig, BatchTarget, RunMode, RunPreset, RunPresetsResp } from '../../api/types'
import { PolicyPage } from './OUPolicyWorkspace'
import InheritField from './InheritField'
import LoadGate from '../../components/LoadGate'
import SettingsWorkspace, { SettingsPanel } from './SettingsWorkspace'
import SettingsSaveBar from './SettingsSaveBar'
import SettingsField from './SettingsField'
import useSettingsDraft from './useSettingsDraft'
import type { ReactElement, ReactNode } from 'react'

// The run form's defaults, on their own page (docs/adr/0014 §4): what the run dialog opens on before the
// user touches anything — which workflow, which mode button, which preset window, whether idle mode
// is pre-checked, how many failure retries, and whether the done-email is pre-ticked.
//
// These started as two rows on the run/queue settings page, next to the queue's own concurrency
// and priority knobs. They are a different kind of setting: nothing here governs how the queue
// behaves, it only decides what the dialog is already holding when it opens, and every one of them
// is a suggestion the user can override in the form. Hence a page of their own — and hence the
// "no default" option on every picker, which is what an unconfigured portal does and stays a
// first-class choice rather than an unset field.
const RUN_FIELDS = {
  targetId: 'run_default_target_id', mode: 'run_default_mode', presetId: 'run_default_preset_id',
  idle: 'run_default_idle', retries: 'run_default_retries', notify: 'run_default_notify',
  showRule: 'run_show_preset_rule',
} as const
interface OUDefaults {
  effective: BatchConfig
  inherited: Record<string, unknown>
  sources: Record<string, string>
  overrides: Record<string, unknown>
}
export default function RunDefaultsPage() {
  return <PolicyPage section="runs" renderOU={group => <RunDefaultsEditor key={group.id} ou={group.id} />}><RunDefaultsEditor /></PolicyPage>
}
function RunDefaultsEditor({ ou }: { ou?: number }) {
  const { t } = useTranslation()
  const { catchLoad, retryKey } = useLoadFailure()
  const { message } = App.useApp()
  const draft = useSettingsDraft({ targetId: 0, mode: 'now' as RunMode, presetId: 0, idle: false, retries: 0, notify: false, showRule: false, inherit: {} as Record<string, boolean> })
  const { targetId, mode, presetId, idle, retries, notify, showRule } = draft.value
  const [ouInfo, setOUInfo] = useState<OUDefaults | null>(null)
  const [saving, setSaving] = useState(false)
  const [section, setSection] = useState('launch')
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [presets, setPresets] = useState<RunPreset[]>([])
  // Same rule as the other settings pages: until the server has answered, the numbers above are
  // this file's opinion and not the portal's configuration, so nothing renders behind the gate.
  // The two lists are part of the gate too — a workflow picker that opens empty and fills in a
  // moment later would show "no default" for a portal that has one.
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return Promise.all([
      api.get<BatchConfig | OUDefaults>(ou ? `/api/admin/batch/defaults/${ou}` : '/api/admin/batch/config'),
      api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets'),
      api.get<RunPresetsResp>('/api/admin/batch/presets'),
    ])
      .then(([result, tg, ps]) => {
        const info = ou ? result as OUDefaults : null
        const cfg = info?.effective ?? result as BatchConfig
        setOUInfo(info)
        draft.accept({ targetId: cfg.run_default_target_id ?? 0, mode: cfg.run_default_mode ?? 'now',
          presetId: cfg.run_default_preset_id ?? 0, idle: !!cfg.run_default_idle,
          retries: cfg.run_default_retries ?? 0, notify: !!cfg.run_default_notify, showRule: !!cfg.run_show_preset_rule, inherit:Object.fromEntries(Object.entries(RUN_FIELDS).map(([key,wire]) => [key,info ? info.overrides[wire] == null : false])) })
        setTargets(tg.targets || [])
        setPresets(ps.presets || [])
      })
      .then(...settleOnAnswer(() => setLoading(false)))
      .catch(catchLoad(setLoadErr))
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [retryKey])

  const save = async () => {
    setSaving(true)
    try {
      if (ou) {
        const overrides = Object.fromEntries(Object.entries(RUN_FIELDS).map(([key, wire]) => [
          wire, draft.value.inherit[key] ? null : draft.value[key as keyof typeof RUN_FIELDS],
        ]))
        await api.put(`/api/admin/batch/defaults/${ou}`, overrides)
      } else {
        await api.post('/api/admin/batch/config', {
          run_default_target_id: targetId,
          run_default_mode: mode,
          run_default_preset_id: presetId,
          run_default_idle: idle,
          run_default_retries: retries,
          run_default_notify: notify,
          run_show_preset_rule: showRule,
        })
      }
      draft.acknowledge(draft.value)
      message.success(t('common.saved'))
    } catch (e) { message.error(errText(e, t)) } finally { setSaving(false) }
  }

  // Only a workflow the run dialog can actually offer is worth defaulting to. A saved default that has
  // since been hidden from that surface is still listed — marked as hidden — rather than silently
  // dropped from its own picker, so an admin sees the choice they made and can keep or clear it.
  const runnable = useMemo(() => visibleOn(targets, 'run'), [targets])
  const targetOptions = useMemo(() => {
    const opts = runnable.map((tg) => ({ value: tg.id, label: tg.name }))
    const saved = targetId && !runnable.some((tg) => tg.id === targetId) ? targets.find((tg) => tg.id === targetId) : undefined
    if (saved) opts.push({ value: saved.id, label: `${saved.name}（${t('runDefaults.hiddenOnRun')}）` })
    return [{ value: 0, label: t('runDefaults.none') }, ...opts]
  }, [runnable, targets, targetId, t])

  // Disabled windows stay in the list, marked: the run form won't offer one, but an admin who has
  // switched a window off for now should still see it waiting here instead of losing the setting.
  const presetOptions = useMemo(
    () => [
      { value: 0, label: t('runDefaults.none') },
      ...presets.map((p) => ({
        value: p.id,
        label: `${p.label} · ${presetSummary(p, t)}${p.enabled ? '' : `（${t('runDefaults.presetOff')}）`}`,
      })),
    ],
    [presets, t],
  )

  const row = (key: keyof typeof RUN_FIELDS, label: string, hint: ReactNode, control: ReactElement<{ 'aria-label'?: string; options?: { value: unknown; label: ReactNode }[] }>) => {
    if (!ou || !ouInfo) return <SettingsField label={label} hint={hint}>{control}</SettingsField>
    const wire = RUN_FIELDS[key]
    const inherited = ouInfo.inherited[wire]
    const option = control.props.options?.find(item => item.value === inherited)
    const formatted = option ? String(option.label) : typeof inherited === 'boolean' ? t(inherited ? 'runDefaults.enabled' : 'runDefaults.disabled') : String(inherited ?? '')
    return <InheritField label={label} hint={hint} inherited={formatted}
      from={ouInfo.sources[wire] || t('ou.globalScope')} inheriting={draft.value.inherit[key] ?? true}
      onInheritingChange={value => draft.patch({
        ...(!value ? { [key]: ouInfo.inherited[wire] } : {}),
        inherit: { ...draft.value.inherit, [key]: value },
      })}>
      {cloneElement(control, { 'aria-label': label })}
    </InheritField>
  }

  return (
    <>
      <LoadGate loading={loading} error={loadErr} onRetry={load}>
        <Typography.Paragraph type="secondary">{t('runDefaults.hint')}</Typography.Paragraph>
        <ConfigProvider componentDisabled={saving}>
        <SettingsWorkspace active={section} onChange={setSection} sections={[
          { key: 'launch', label: t('runDefaults.section.launch') }, { key: 'completion', label: t('runDefaults.section.completion') },
        ]}>
          <SettingsPanel section="launch">
          {row('targetId',
            t('runDefaults.workflow'),
            t('runDefaults.workflowHint'),
            <Select value={targetId} onChange={(targetId) => draft.patch({ targetId })} options={targetOptions} />,
          )}
          {row('mode',
            t('runDefaults.mode'),
            t('runDefaults.modeHint'),
            <Select
              value={mode}
              onChange={(mode) => draft.patch({ mode })}
              style={{ width: 140 }}
              options={[
                { value: 'now', label: t('run.now') },
                { value: 'preset', label: t('run.preset') },
                { value: 'scheduled', label: t('run.scheduled') },
              ]}
            />,
          )}
          {row('presetId',
            t('runDefaults.preset'),
            // A portal with no windows configured yet gets the way to make one instead of a hint
            // about a picker that can only say "no default".
            presets.length === 0 ? (
              <>
                {t('runDefaults.noPresets')} <Link to="/manage/runqueue">{t('runDefaults.managePresets')}</Link>
              </>
            ) : (
              t('runDefaults.presetHint')
            ),
            <Select
              value={presetId}
              onChange={(presetId) => draft.patch({ presetId })}
              disabled={presets.length === 0}
              options={presetOptions}
            />,
          )}
          {row('showRule',
            t('runDefaults.showRule'),
            t('runDefaults.showRuleHint'),
            <Switch checked={showRule} onChange={(showRule) => draft.patch({ showRule })} disabled={presets.length === 0} />,
          )}
          {row('idle', t('runDefaults.idle'), t('runDefaults.idleHint'), <Switch checked={idle} onChange={(idle) => draft.patch({ idle })} />)}
          </SettingsPanel>
          <SettingsPanel section="completion">
          {row('retries',
            t('runDefaults.retries'),
            t('runDefaults.retriesHint'),
            <InputNumber min={0} max={5} value={retries} onChange={(v) => draft.patch({ retries: v ?? 0 })} />,
          )}
          {row('notify', t('runDefaults.notify'), t('runDefaults.notifyHint'), <Switch checked={notify} onChange={(notify) => draft.patch({ notify })} />)}
          </SettingsPanel>
        </SettingsWorkspace>
        </ConfigProvider>
        <SettingsSaveBar dirty={draft.dirty} saving={saving} onSave={save} onDiscard={draft.discard} />
      </LoadGate>
    </>
  )
}
