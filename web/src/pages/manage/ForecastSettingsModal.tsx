import { useEffect, useState } from 'react'
import { App, Alert, Input, Modal, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { ForecastSettings } from '../../api/types'

// The evaluator's settings, as a modal opened from the verification page's header.
//
// Two settings and no more, because that is the whole of what the evaluator reads:
//
//   - whether it runs at all. It is a background loop that reaches a Chinese market-data vendor on
//     its own volition, on every install where it is on, which is why it ships OFF (ADR 0029 §6) and
//     why flipping it is audited — the operator turned on something that egresses.
//   - the benchmark NEW sets are written with. It is here rather than on the page because it is a
//     write-once-per-set fact: changing it never re-scores anything already on the ledger, and the
//     hint says so before an admin assumes otherwise.
//
// The counters the page shows (due, pending, refused, unsettled) are deliberately NOT editable and
// not repeated here: they are what the evaluator has done, not what it is configured to do.
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
  const [enabled, setEnabled] = useState(value.enabled)
  const [bench, setBench] = useState(value.bench)
  const [busy, setBusy] = useState(false)

  // Seed the draft from the page's copy each time the modal opens. Without this, a modal closed
  // after a rejected save would reopen holding the value that was refused rather than the one the
  // server still has.
  useEffect(() => {
    if (open) {
      setEnabled(value.enabled)
      setBench(value.bench)
    }
  }, [open, value.enabled, value.bench])

  const save = async () => {
    setBusy(true)
    try {
      await api.post('/api/admin/forecast', { enabled, bench })
      message.success(t('common.saved'))
      onSaved()
      onClose()
    } catch (e) {
      // The server owns the benchmark vocabulary (a six-digit index with a daily series), so its
      // refusal is shown with its own code — err.forecast_bad_bench — rather than a generic failure.
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
        {/* The disclosure, stated before the switch rather than after it: with this on, the loop
            calls a third-party vendor on its own schedule for every due set. An operator cannot
            weigh what they have not been told, and a one-line hint under a control is not a
            substitute for saying it plainly. */}
        <Alert type="warning" showIcon title={t('forecast.settings.enabledWarn')} />
        <Space wrap>
          <Switch checked={enabled} onChange={setEnabled} aria-label={t('forecast.settings.enabled')} />
          <Typography.Text>{t('forecast.settings.enabled')}</Typography.Text>
          <Typography.Text type="secondary">{t('forecast.settings.enabledHint')}</Typography.Text>
        </Space>
        <div>
          <Typography.Text>{t('forecast.settings.bench')}</Typography.Text>
          <Input
            value={bench}
            onChange={(e) => setBench(e.target.value)}
            placeholder="sh000852"
            aria-label={t('forecast.settings.bench')}
            style={{ marginTop: 4 }}
          />
          <Typography.Text type="secondary" style={{ display: 'block', marginTop: 4 }}>
            {t('forecast.settings.benchHint')}
          </Typography.Text>
        </div>
      </Space>
    </Modal>
  )
}
