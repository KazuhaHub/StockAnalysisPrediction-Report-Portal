import { useEffect, useState } from 'react'
import { App, Button, Modal, Select, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { ForecastSettings } from '../../api/types'

// The evaluator's settings, as a modal opened from the verification page's header.
//
// The benchmark setting. Since P9 the evaluator's ON/OFF/DRY switch lives on the operating card
// beside the readiness list that gates it, so this modal has one job: the index NEW sets are written
// with. A change is effective from the NEXT cohort (P4): the sets already written keep the index they
// were frozen with, so nothing on the ledger is re-scored. The modal posts only the fields the admin
// actually changed.
export default function ForecastSettingsModal({
  open,
  value,
  onClose,
  onSaved,
}: {
  open: boolean
  value: ForecastSettings
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [bench, setBench] = useState(value.bench_next || value.bench)
  const [busy, setBusy] = useState(false)

  // Seed the draft from the page's copy each time the modal opens. Without this, a modal closed
  // after a rejected save would reopen holding the value that was refused rather than the one the
  // server still has.
  useEffect(() => {
    if (open) {
      setBench(value.bench_next || value.bench)
    }
  }, [open, value.bench, value.bench_next])

  const benchOptions = value.bench_codes.map((code) => ({
    value: code,
    label: t(`forecast.bench.${code}`),
  }))

  const save = async () => {
    const payload: Record<string, unknown> = {}
    const chosen = value.bench_next || value.bench
    if (bench !== chosen) payload.bench_schedule = bench
    if (Object.keys(payload).length === 0) {
      onClose()
      return
    }
    setBusy(true)
    try {
      await api.post('/api/admin/forecast', payload)
      message.success(t('common.saved'))
      onSaved()
      onClose()
    } catch (e) {
      // The server owns the benchmark vocabulary (an index with a daily series), so its refusal is
      // shown with its own code — err.forecast_bad_bench — rather than a generic failure.
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  const cancelSchedule = async () => {
    setBusy(true)
    try {
      await api.post('/api/admin/forecast', { bench_cancel: true })
      message.success(t('common.saved'))
      onSaved()
      onClose()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={t('forecast.settings.title')}
      open={open}
      onCancel={onClose}
      onOk={save}
      confirmLoading={busy}
      okText={t('common.save')}
      cancelText={t('common.cancel')}
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <div>
          <Typography.Text>{t('forecast.settings.bench')}</Typography.Text>
          <Select
            value={bench}
            onChange={setBench}
            options={benchOptions}
            aria-label={t('forecast.settings.bench')}
            style={{ width: '100%', marginTop: 4 }}
          />
          <Typography.Text type="secondary" style={{ display: 'block', marginTop: 4 }}>
            {t('forecast.settings.benchHint', { active: t(`forecast.bench.${value.bench}`) })}
          </Typography.Text>
          {value.bench_next && value.bench_next_from && (
            <Typography.Text style={{ display: 'block', marginTop: 4 }}>
              {t('forecast.settings.scheduled', {
                bench: t(`forecast.bench.${value.bench_next}`),
                from: value.bench_next_from,
              })}{' '}
              <Button type="link" size="small" onClick={cancelSchedule} disabled={busy}>
                {t('forecast.settings.scheduledCancel')}
              </Button>
            </Typography.Text>
          )}
        </div>
      </Space>
    </Modal>
  )
}
