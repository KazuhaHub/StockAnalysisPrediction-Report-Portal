import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, InputNumber, Popconfirm, Space, Spin, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'

export default function LoginHistoryRetentionCard() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [saved, setSaved] = useState<number | null>(null)
  const [keep, setKeep] = useState<number | null>(null)
  const [failed, setFailed] = useState(false)
  const [saving, setSaving] = useState(false)
  const load = () => {
    setFailed(false)
    return api.get<{ keep: number }>('/api/admin/login-activity/retention')
      .then((r) => { setSaved(r.keep); setKeep(r.keep) })
      .catch(() => setFailed(true))
  }
  useEffect(() => { void load() }, [])
  const valid = keep !== null && Number.isInteger(keep) && keep >= 1 && keep <= 10000
  const save = async () => {
    if (!valid) return
    setSaving(true)
    try {
      await api.put('/api/admin/login-activity/retention', { keep })
      setSaved(keep)
      message.success(t('common.saved'))
    } catch (e) { message.error(errText(e, t)) }
    finally { setSaving(false) }
  }
  return <Card title={t('storage.loginHistoryTitle')}>
    {failed ? <Alert type="error" title={t('loginActivity.loadFailed')} action={<Button onClick={() => void load()}>{t('common.retry')}</Button>} /> : saved === null ? <Spin /> : <>
      <Space wrap>
        <Typography.Text>{t('loginActivity.keep')}</Typography.Text>
        <InputNumber aria-label={t('loginActivity.keep')} min={1} max={10000} precision={0} value={keep} onChange={setKeep} disabled={saving} />
        <Popconfirm title={t('storage.loginHistoryConfirm', { count: keep })} onConfirm={save}>
          <Button type="primary" loading={saving} disabled={!valid || keep === saved}>{t('common.save')}</Button>
        </Popconfirm>
      </Space>
      <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>{t('storage.loginHistoryHint')}</Typography.Paragraph>
    </>}
  </Card>
}
