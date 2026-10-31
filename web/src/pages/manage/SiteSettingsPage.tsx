import { useEffect, useState, type ReactNode } from 'react'
import { App, Button, Form, Input, Radio, Select, Space, Switch, Typography, Upload } from 'antd'
import { DeleteOutlined, UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import { useLoadFailure } from '../../lib/useOnReconnect'
import { settleOnAnswer } from '../../lib/loadFailure'
import type { SettingsResp, UpdatePromptPolicy, VersionDisplay } from '../../api/types'
import { useSite } from '../../site'
import { BrandIcon } from '../../components/icons'
import LoadGate from '../../components/LoadGate'
import GeoSection from './GeoSection'
import SettingsWorkspace, { SettingsPanel } from './SettingsWorkspace'
import SettingsSaveBar from './SettingsSaveBar'
import { notifyError } from '../../lib/notify'

// tzOptions lists the panel-timezone choices: a "follow system" default plus the
// browser's full IANA zone list (Intl.supportedValuesOf, guarded for older engines).
function tzOptions(systemLabel: string) {
  let zones: string[] = []
  try {
    zones = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? []
  } catch {
    zones = []
  }
  return [{ value: '', label: systemLabel }, ...zones.map((z) => ({ value: z, label: z }))]
}

// The update-prompt policies, in the order the page presents them: least intrusive first.
const UPDATE_POLICIES: UpdatePromptPolicy[] = ['dismissible', 'persistent', 'required', 'automatic']
const VERSION_DISPLAYS: VersionDisplay[] = ['hidden', 'footer', 'header']

// Site branding, PWA, footer, panel timezone and the update prompt. Announcement lives on
// its own page now; each page posts only its own fields and the settings API merges
// per-field (nil = untouched), so saving here never disturbs the announcement.
export default function SiteSettingsPage() {
  const { t } = useTranslation()
  const { catchLoad, retryKey } = useLoadFailure({ guardDrafts: true })
  const { message } = App.useApp()
  const { refresh } = useSite()
  const [form] = Form.useForm()
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')
  const [saving, setSaving] = useState(false)
  const [section, setSection] = useState('branding')
  const [savedValues, setSavedValues] = useState<Record<string, unknown> | null>(null)
  const draft = Form.useWatch([], { form, preserve: true }) as Record<string, unknown> | undefined
  const dirty = !!savedValues && Object.keys(savedValues).some((key) => draft?.[key] !== savedValues[key])
  const sections = [
    { key: 'branding', label: t('manage.section.branding') },
    { key: 'pwa', label: t('settings.pwaEnabled') },
    { key: 'footer', label: t('settings.footerSection') },
    { key: 'updates', label: t('settings.updatePrompt') },
    { key: 'url', label: t('settings.urlSection') },
    { key: 'time', label: t('settings.timeSection') },
    { key: 'geo', label: t('audit.geoTitle') },
  ]

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return api
      .get<SettingsResp>('/api/admin/settings')
      .then((r) => {
        const values = {
          siteTitle: r.siteTitle || '',
          siteLogoUrl: r.siteLogoUrl || '',
          footerText: r.footerText || '',
          footerShowInfo: r.footerShowInfo !== false,
          versionDisplay: r.versionDisplay || 'footer',
          updatePromptPolicy: r.updatePromptPolicy || 'dismissible',
          pwaEnabled: r.pwaEnabled !== false,
          pwaIconUrl: r.pwaIconUrl || '',
          timezone: r.timezone || '',
          publicUrl: r.publicUrl || '',
        }
        form.setFieldsValue(values)
        setSavedValues(values)
      })
      // The spinner covered the wait but not the failure: `finally` alone dropped the rejection
      // and left an empty form claiming this portal has no title, no logo and no public URL —
      // in a form whose Save button would write exactly that.
      .then(...settleOnAnswer(() => setLoading(false)))
      .catch(catchLoad(setLoadErr))
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form, retryKey])

  const save = async () => {
    let v
    try {
      v = await form.validateFields()
    } catch (error) {
      const field = String((error as { errorFields?: { name: (string | number)[] }[] }).errorFields?.[0]?.name[0] ?? '')
      const fieldSections: Record<string, string> = { siteTitle: 'branding', footerText: 'footer', publicUrl: 'url' }
      if (fieldSections[field]) setSection(fieldSections[field])
      return
    }
    setSaving(true)
    try {
      await api.post('/api/admin/settings', {
        siteTitle: v.siteTitle || '',
        siteLogoUrl: v.siteLogoUrl || '',
        footerText: v.footerText || '',
        footerShowInfo: v.footerShowInfo !== false,
        versionDisplay: v.versionDisplay || 'footer',
        updatePromptPolicy: v.updatePromptPolicy || 'dismissible',
        pwaEnabled: v.pwaEnabled !== false,
        pwaIconUrl: v.pwaIconUrl || '',
        timezone: v.timezone || '',
        publicUrl: v.publicUrl || '',
      })
      await refresh()
      setSavedValues({ ...v })
      message.success(t('common.saved'))
    } catch (error) {
      notifyError(errText(error, t))
    } finally {
      setSaving(false)
    }
  }

  const uploadAsset = async (kind: 'logo' | 'pwaIcon', file: File, field: 'siteLogoUrl' | 'pwaIconUrl') => {
    const fd = new FormData()
    fd.set('kind', kind)
    fd.set('file', file)
    const r = await api.upload<{ url: string }>('/api/admin/site-asset', fd)
    form.setFieldsValue({ [field]: r.url })
    message.success(t('common.done'))
  }

  const uploadLogo = (file: File) => {
    if (!file.type.startsWith('image/')) {
      notifyError(t('settings.logoTypeInvalid'))
      return false
    }
    if (file.size > 512 * 1024) {
      notifyError(t('settings.logoTooLarge'))
      return false
    }
    uploadAsset('logo', file, 'siteLogoUrl').catch((e) => notifyError(e instanceof Error ? e.message : t('settings.logoReadFailed')))
    return false
  }

  const uploadPwaIcon = (file: File) => {
    if (!file.type.startsWith('image/')) {
      notifyError(t('settings.logoTypeInvalid'))
      return false
    }
    if (file.size > 512 * 1024) {
      notifyError(t('settings.pwaIconTooLarge'))
      return false
    }
    uploadAsset('pwaIcon', file, 'pwaIconUrl').catch((e) => notifyError(e instanceof Error ? e.message : t('settings.logoReadFailed')))
    return false
  }

  return (
      <LoadGate loading={loading} error={loadErr} onRetry={load}>
      <Form form={form} layout="vertical" disabled={saving}>
        <SettingsWorkspace sections={sections} active={section} onChange={setSection}>
        <SettingsPanel section="branding">
        <Form.Item
          name="siteTitle"
          label={t('settings.siteTitle')}
          rules={[{ max: 80, message: t('settings.siteTitleTooLong') }]}
        >
          <Input maxLength={80} showCount placeholder={t('brand')} />
        </Form.Item>
        <ImageField
          name="siteLogoUrl"
          label={t('settings.logoUrl')}
          hint={t('settings.logoHint')}
          upload={uploadLogo}
          uploadLabel={t('settings.logoUpload')}
          clearLabel={t('settings.logoClear')}
          onClear={() => form.setFieldsValue({ siteLogoUrl: '' })}
          preview={(get) => {
            const logo = String(get('siteLogoUrl') || '').trim()
            const title = String(get('siteTitle') || '').trim() || t('brand')
            return (
              <>
                {logo ? (
                  <img src={logo} alt="" style={{ width: 28, height: 28, objectFit: 'contain' }} />
                ) : (
                  <BrandIcon style={{ color: 'var(--ant-color-primary)', fontSize: 28 }} />
                )}
                <Typography.Text strong ellipsis>{title}</Typography.Text>
              </>
            )
          }}
        />
        </SettingsPanel>
        <SettingsPanel section="pwa">
        <Form.Item name="pwaEnabled" label={t('settings.pwaEnabled')} valuePropName="checked">
          <Switch />
        </Form.Item>
        <ImageField
          name="pwaIconUrl"
          label={t('settings.pwaIconUrl')}
          hint={t('settings.pwaIconHint')}
          upload={uploadPwaIcon}
          uploadLabel={t('settings.pwaIconUpload')}
          clearLabel={t('settings.pwaIconClear')}
          onClear={() => form.setFieldsValue({ pwaIconUrl: '' })}
          preview={(get) => {
            const icon = String(get('pwaIconUrl') || get('siteLogoUrl') || '').trim()
            return icon ? (
              <img src={icon} alt={t('settings.pwaIconPreview')} style={{ width: 40, height: 40, objectFit: 'contain', borderRadius: 'var(--ant-border-radius)' }} />
            ) : (
              <BrandIcon aria-label={t('settings.pwaIconPreview')} style={{ color: 'var(--ant-color-primary)', fontSize: 40 }} />
            )
          }}
        />
        </SettingsPanel>
        <SettingsPanel section="footer">
        <Form.Item name="footerShowInfo" label={t('settings.footerShowInfo')} valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item
          name="footerText"
          label={t('settings.footerText')}
          extra={t('settings.footerTextHint')}
          rules={[{ max: 1000, message: t('settings.footerTextTooLong') }]}
        >
          <Input.TextArea
            maxLength={1000}
            showCount
            autoSize={{ minRows: 2, maxRows: 4 }}
            placeholder={t('settings.footerTextPlaceholder')}
          />
        </Form.Item>
        <Form.Item name="versionDisplay" label={t('settings.versionDisplay')}>
          <Radio.Group>
            <Space orientation="vertical" size={8}>
              {VERSION_DISPLAYS.map((placement) => (
                <Radio key={placement} value={placement}>
                  {t(`settings.versionDisplay.${placement}`)}
                </Radio>
              ))}
            </Space>
          </Radio.Group>
        </Form.Item>
        {/* One radio group, not a pair of switches: "may the reader defer" and "must they refresh"
            are the same decision, and two booleans can describe a state nobody chose. */}
        </SettingsPanel>
        <SettingsPanel section="updates">
        <Form.Item name="updatePromptPolicy" style={{ marginBottom: 8 }}>
          <Radio.Group>
            <Space orientation="vertical" size={10}>
              {UPDATE_POLICIES.map((p) => (
                <Radio key={p} value={p}>
                  <span>{t(`settings.policy.${p}`)}</span>
                  <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: '2px 0 0', maxWidth: 560 }}>
                    {t(`settings.policy.${p}Hint`)}
                  </Typography.Paragraph>
                </Radio>
              ))}
            </Space>
          </Radio.Group>
        </Form.Item>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          {t('settings.updatePromptHint')}
        </Typography.Paragraph>
        {/* The portal's own origin. It used to sit on the email page, because reset links were the
            first thing that needed an origin a forged Host header cannot poison — but the SAML
            entity id, the OIDC redirect URL, the WebAuthn relying-party id, registration links and
            the captcha host check all derive from it too, so it belongs with the deployment. */}
        </SettingsPanel>
        <SettingsPanel section="url">
        <Form.Item
          name="publicUrl"
          label={t('settings.publicUrl')}
          style={{ marginBottom: 8 }}
          rules={[
            {
              validator: (_, v?: string) =>
                !v || /^https?:\/\/[^/?#]+\/?$/.test(v.trim())
                  ? Promise.resolve()
                  : Promise.reject(new Error(t('settings.publicUrlInvalid'))),
            },
          ]}
        >
          <Input placeholder="https://portal.example.com" />
        </Form.Item>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          {t('settings.publicUrlHint')}
        </Typography.Paragraph>

        </SettingsPanel>
        <SettingsPanel section="time">
        <Form.Item name="timezone" label={t('settings.timezone')} style={{ marginBottom: 8 }}>
          <Select
            showSearch
            options={tzOptions(t('settings.timezoneSystem'))}
            style={{ width: '100%' }}
            filterOption={(input, opt) => (opt?.label ?? '').toLowerCase().includes(input.toLowerCase())}
          />
        </Form.Item>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          {t('settings.timezoneHint')}
        </Typography.Paragraph>
        {/* The IP database. It describes the DEPLOYMENT — like the public URL above it — rather
            than belonging to the one page that happens to display its output. */}
        </SettingsPanel>
        <SettingsPanel section="geo"><GeoSection /></SettingsPanel>
        </SettingsWorkspace>
        <SettingsSaveBar dirty={dirty} saving={saving} onSave={save}
          onDiscard={() => { if (savedValues) { form.setFieldsValue(savedValues); form.setFields(Object.keys(savedValues).map((name) => ({ name, errors: [] }))) } }} />
      </Form>
      </LoadGate>
  )
}

// An image setting: what it looks like now, beside the buttons that change it, and the address it is
// read from below them for anyone who would rather point at an existing file.
function ImageField({ name, label, hint, upload, uploadLabel, clearLabel, onClear, preview }: {
  name: string
  label: string
  hint: string
  upload: (file: File) => boolean | void
  uploadLabel: string
  clearLabel: string
  onClear: () => void
  preview: (get: (name: string) => unknown) => ReactNode
}) {
  const { t } = useTranslation()
  return (
    <Form.Item label={label} extra={hint} htmlFor={`rp-image-${name}`}>
      <div className="rp-image-field">
        <Form.Item shouldUpdate noStyle>
          {({ getFieldValue }) => <div className="rp-image-field__preview">{preview(getFieldValue)}</div>}
        </Form.Item>
        <Space wrap size={8}>
          <Upload accept="image/*" showUploadList={false} beforeUpload={upload}>
            <Button icon={<UploadOutlined />}>{uploadLabel}</Button>
          </Upload>
          <Button icon={<DeleteOutlined />} onClick={onClear}>{clearLabel}</Button>
        </Space>
      </div>
      <Form.Item name={name} noStyle>
        <Input id={`rp-image-${name}`} placeholder={t('settings.logoPlaceholder')} style={{ marginTop: 12 }} />
      </Form.Item>
    </Form.Item>
  )
}
