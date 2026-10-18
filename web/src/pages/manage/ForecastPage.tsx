import { useEffect, useState } from 'react'
import { Alert, Button, Card, Descriptions, Space, Tag, Typography } from 'antd'
import { SettingOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { ForecastSettings } from '../../api/types'
import LoadGate from '../../components/LoadGate'
import { readPrefetched } from '../../lib/prefetch'
import ForecastSettingsModal from './ForecastSettingsModal'

// 预测验证 (forecast verification): the admin page for the prediction ledger (ADR 0029).
//
// This is the page FRAMEWORK — its route, its navigation entry, the evaluator's settings, and the
// status of what the evaluator has done. The statistical matrix (the monthly view, the custom-range
// tab, the drawer over a mature cell) is PR 7 and lands inside this page; the endpoint behind it is
// PR 6. What ships here is everything that does not depend on that number: whether the evaluator
// runs, what it is catching up on, and the one control an operator needs to turn it on.
//
// Why the evaluator's own status is on the page at all: the switch is off by default, so a portal
// where the feature is configured but nothing is settling has to be distinguishable at a glance from
// one where it is simply switched off (ADR 0029 §6). The counters answer that — "on and caught up"
// reads as zeros or as a small pending count, while "on and stuck" is a refused or unsettled-baseline
// count that has stopped moving.

// How stale a hover-prefetched answer may be and still be shown while the real request is in flight.
// Same window as the run dialog: long enough to cover the hover-to-click gap, short enough that what
// is on screen is never meaningfully behind what the server holds.
const WARM_MAX_AGE = 5 * 60_000

export default function ForecastPage() {
  const { t } = useTranslation()
  const [settings, setSettings] = useState<ForecastSettings | null>(null)
  const [loading, setLoading] = useState(true)
  // Separate from `loading` for the reason the other consoles record: the reload behind a save must
  // refresh in place. Replacing a page that has just saved with a full-page load error, because the
  // refresh behind it failed, throws away what the admin was reading.
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return api
      .get<ForecastSettings>('/api/admin/forecast')
      .then((st) => {
        setSettings(st)
        setLoaded(true)
      })
      .catch((e) => setLoadErr(errText(e, t)))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    // The shell warms this GET when its menu item is hovered (ManageLayout's WARM_ON_HOVER), so a
    // warm answer lets the page render filled instead of spinning. It decides only whether this
    // render waits — the request below goes out either way and replaces it, so a count that moved
    // since the hover corrects itself on screen.
    const warm = readPrefetched<ForecastSettings>('/api/admin/forecast', WARM_MAX_AGE)
    if (warm) {
      setSettings(warm)
      setLoaded(true)
      setLoading(false)
    }
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const row = (label: string, value: React.ReactNode) => (
    <Descriptions.Item label={label}>{value}</Descriptions.Item>
  )

  return (
    <LoadGate loading={loading && !loaded} error={loaded ? undefined : loadErr} onRetry={load}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ flex: '1 1 320px', minWidth: 0 }}>
            <Typography.Title level={4} style={{ margin: 0 }}>
              {t('forecast.page.title')}
            </Typography.Title>
            <Typography.Text type="secondary">{t('forecast.page.subtitle')}</Typography.Text>
          </div>
          <Space wrap>
            {/* Only 评估设置 for now. The plan's other header button is 导出 CSV, and it exports the
                statistical matrix this PR does not have yet — a button that exported nothing would be
                worse than its absence, so it arrives with the matrix in PR 7. */}
            <Button icon={<SettingOutlined />} onClick={() => setSettingsOpen(true)}>
              {t('forecast.settings.button')}
            </Button>
          </Space>
        </div>

        <Card title={t('forecast.eval.title')}>
          {settings && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              {/* The switch's own state, first and in words. A page whose whole point is "is this
                  running" must not make an operator read a number to find out. */}
              {settings.enabled ? (
                <Tag color="green">{t('forecast.eval.on')}</Tag>
              ) : (
                <Tag>{t('forecast.eval.off')}</Tag>
              )}
              {/* Off is the shipped state and reads as a fault unless it is explained, which is what
                  this alert is for: nothing is wrong, the evaluator simply has not been turned on. */}
              {!settings.enabled && <Alert type="info" showIcon title={t('forecast.eval.offHint')} />}
              <Descriptions column={1} size="small" bordered>
                {row(t('forecast.eval.bench'), <Typography.Text code>{settings.bench}</Typography.Text>)}
                {row(t('forecast.eval.due'), settings.due_sets)}
                {row(t('forecast.eval.pending'), settings.pending)}
                {/* A refusal count is the one number here that can mean work is stuck rather than
                    waiting, so it is coloured when it is non-zero — the same treatment the audit and
                    storage consoles give a non-zero count. */}
                {row(
                  t('forecast.eval.refused'),
                  settings.refused > 0 ? <Tag color="red">{settings.refused}</Tag> : 0,
                )}
                {row(
                  t('forecast.eval.unsettled'),
                  settings.unsettled_baseline > 0 ? (
                    <Tag color="orange">{settings.unsettled_baseline}</Tag>
                  ) : (
                    0
                  ),
                )}
              </Descriptions>
              <Typography.Text type="secondary">{t('forecast.eval.hint')}</Typography.Text>
            </div>
          )}
        </Card>

        {/* The matrix is PR 7 and its data is PR 6. Stated on the page rather than left as blank
            space, so an operator who opened this page expecting numbers is told where they are
            rather than left to wonder whether the page failed to load. */}
        <Card title={t('forecast.matrix.title')}>
          <Typography.Text type="secondary">{t('forecast.matrix.pending')}</Typography.Text>
        </Card>
      </div>

      {settings && (
        <ForecastSettingsModal
          open={settingsOpen}
          value={settings}
          onClose={() => setSettingsOpen(false)}
          onSaved={load}
        />
      )}
    </LoadGate>
  )
}
