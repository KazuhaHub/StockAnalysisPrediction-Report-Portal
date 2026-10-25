import { useEffect, useState } from 'react'
import { Alert, App, Button, ConfigProvider, Input, InputNumber, Select, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import LoadGate from '../../components/LoadGate'
import SettingsWorkspace, { SettingsPanel } from './SettingsWorkspace'
import SettingsField from './SettingsField'
import SettingsSaveBar from './SettingsSaveBar'
import useSettingsDraft from './useSettingsDraft'

interface EmailConfig { enabled: boolean; host: string; port: number; user: string; from: string; security: string; has_pass: boolean }

export default function EmailPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const draft = useSettingsDraft({ enabled: false, host: '', port: 587, user: '', from: '', security: 'starttls' })
  const [pass, setPass] = useState('')
  const [hasPass, setHasPass] = useState(false)
  const [testTo, setTestTo] = useState('')
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')
  const [section, setSection] = useState('connection')
  const dirty = draft.dirty || !!pass
  const load = () => {
    setLoading(true); setLoadErr('')
    return api.get<EmailConfig>('/api/admin/email').then((r) => {
      draft.accept({ enabled: r.enabled, host: r.host, port: r.port || 587, user: r.user, from: r.from, security: r.security || 'starttls' })
      setHasPass(r.has_pass); setPass('')
    }).catch((e) => setLoadErr(errText(e, t))).finally(() => setLoading(false))
  }
  useEffect(() => { void load() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const save = async () => {
    setSaving(true)
    try {
      await api.post('/api/admin/email', { ...draft.value, pass })
      draft.acknowledge(draft.value)
      if (pass) setHasPass(true)
      setPass('')
      message.success(t('common.saved'))
    } catch (e) { message.error(errText(e, t)) } finally { setSaving(false) }
  }
  const sendTest = async () => {
    setTesting(true)
    try {
      await api.post('/api/admin/email/test', { to: testTo })
      message.success(t('email.testSent'))
    } catch (e) { message.error(errText(e, t)) } finally { setTesting(false) }
  }
  const v = draft.value
  return <LoadGate loading={loading} error={loadErr} onRetry={load}>
    <SettingsWorkspace active={section} onChange={setSection} sections={[
      { key: 'connection', label: t('email.connection') }, { key: 'test', label: t('email.testSection') },
    ]}>
      <SettingsPanel section="connection">
        <Typography.Paragraph type="secondary">{t('email.hint')}</Typography.Paragraph>
        <ConfigProvider componentDisabled={saving}>
          <SettingsField label={t('email.enabled')}><Switch checked={v.enabled} onChange={(enabled) => draft.patch({ enabled })} /></SettingsField>
          <SettingsField label={t('email.host')}><Input value={v.host} onChange={(e) => draft.patch({ host: e.target.value })} placeholder="smtp.example.com" /></SettingsField>
          <SettingsField label={t('email.port')}><InputNumber min={1} max={65535} value={v.port} onChange={(port) => draft.patch({ port: port || 587 })} /></SettingsField>
          <SettingsField label={t('email.security')}><Select value={v.security} onChange={(security) => draft.patch({ security })} options={[
            { value: 'starttls', label: 'STARTTLS (587)' }, { value: 'tls', label: 'TLS / SSL (465)' }, { value: 'none', label: t('email.securityNone') },
          ]} /></SettingsField>
          <SettingsField label={t('email.user')}><Input value={v.user} onChange={(e) => draft.patch({ user: e.target.value })} autoComplete="off" /></SettingsField>
          <SettingsField label={t('email.pass')}><Input.Password value={pass} onChange={(e) => setPass(e.target.value)} placeholder={hasPass ? t('email.passKeep') : ''} autoComplete="new-password" /></SettingsField>
          <SettingsField label={t('email.from')}><Input value={v.from} onChange={(e) => draft.patch({ from: e.target.value })} placeholder="noreply@example.com" /></SettingsField>
        </ConfigProvider>
      </SettingsPanel>
      <SettingsPanel section="test">
        <Alert type="info" showIcon title={t('email.testSaved')} style={{ marginBottom: 16 }} />
        <Space wrap>
          <Input aria-label={t('email.testTo')} value={testTo} onChange={(e) => setTestTo(e.target.value)} placeholder={t('email.testTo')} style={{ width: 260, maxWidth: '100%' }} />
          <Button loading={testing} disabled={dirty || saving || !v.enabled || !testTo.trim()} onClick={sendTest}>{t('email.test')}</Button>
        </Space>
      </SettingsPanel>
    </SettingsWorkspace>
    <SettingsSaveBar dirty={dirty} saving={saving} onSave={save} onDiscard={() => { draft.discard(); setPass('') }} />
  </LoadGate>
}
