import { useEffect, useState } from 'react'
import { Alert, Button, Card, Descriptions, Space, Tag, Typography } from 'antd'
import { SettingOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api, errText } from '../../api/client'
import type { ForecastSettings } from '../../api/types'
import LoadGate from '../../components/LoadGate'
import { readPrefetched } from '../../lib/prefetch'
import ForecastSettingsModal from './ForecastSettingsModal'
import ForecastAccessSummary from './ForecastAccessSummary'
import ForecastEvaluatorStatus from './ForecastEvaluatorStatus'
import ForecastAlerts from './ForecastAlerts'
import ForecastLedgerStart from './ForecastLedgerStart'
import ForecastDiagnostics from './ForecastDiagnostics'
import SettingsWorkspace, { SettingsPanel } from './SettingsWorkspace'
import { PolicyPage } from './OUPolicyWorkspace'

// Forecast operations: the administrator's page for running the prediction ledger
// (ADR 0029).
//
// It holds what only an operator needs: the evaluator's status and backlog, its settings and the
// benchmark schedule, the ledger start, alerts, the unrecorded count, and a read-only picture of who
// may read the record. The statistics themselves are on /forecast for everyone the access model
// admits, administrators included, so the numbers an operator checks are the numbers a reader sees.

// How stale a hover-prefetched answer may be and still be shown while the real request is in flight.
const WARM_MAX_AGE = 5 * 60_000

export default function ForecastPage() {
  return <PolicyPage section="forecast"><GlobalForecastOperations /></PolicyPage>
}

function GlobalForecastOperations() {
  const { t } = useTranslation()

  const [settings, setSettings] = useState<ForecastSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [ledgerOpen, setLedgerOpen] = useState(false)
  const [section, setSection] = useState('status')

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

  // Empty deps on purpose: the request goes out once, and a reload behind a save calls load()
  // directly. `t` is stable in the app but not under a test double, and a dependency on it would
  // turn a mocked translation into a fetch loop.
  useEffect(() => {
    const warm = readPrefetched<ForecastSettings>('/api/admin/forecast', WARM_MAX_AGE)
    if (warm) {
      setSettings(warm)
      setLoaded(true)
      setLoading(false)
    }
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const row = (label: string, value: React.ReactNode) => <Descriptions.Item label={label}>{value}</Descriptions.Item>

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
            <Link to="/forecast">{t('nav.forecast_record')}</Link>
            <Button icon={<SettingOutlined />} onClick={() => setSettingsOpen(true)}>
              {t('forecast.settings.button')}
            </Button>
            <Button onClick={() => setLedgerOpen(true)}>{t('forecast.population.button')}</Button>
          </Space>
        </div>

        <SettingsWorkspace active={section} onChange={setSection} sections={[
          { key: 'status', label: t('manage.operation.status') }, { key: 'diagnostics', label: t('manage.operation.diagnostics') },
          { key: 'alerts', label: t('manage.operation.alerts') }, { key: 'access', label: t('manage.operation.access') },
        ]}>
        <SettingsPanel section="status">
        <div className="rp-admin-list-page">
        {settings && <ForecastEvaluatorStatus settings={settings} onChanged={load} />}
        <Card title={t('forecast.eval.title')}>
          {settings && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              {settings.enabled ? (
                <Tag color="green">{t('forecast.eval.on')}</Tag>
              ) : (
                <Tag>{t('forecast.eval.off')}</Tag>
              )}
              {!settings.enabled && <Alert type="info" showIcon title={t('forecast.eval.offHint')} />}
              <Descriptions column={1} size="small" bordered>
                {row(t('forecast.eval.bench'), <Typography.Text code>{settings.bench}</Typography.Text>)}
                {row(t('forecast.eval.due'), settings.due_sets)}
                {row(t('forecast.eval.pending'), settings.pending)}
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
              {(settings.unrecorded ?? [])
                .filter((u) => u.ok && (u.n > 0 || u.store_failed_30d > 0))
                .map((u) => (
                  <Alert
                    key={u.rtype}
                    type="warning"
                    showIcon
                    title={
                      <>
                        <Typography.Text strong>{u.rtype}</Typography.Text>{' '}
                        {t('forecast.cov.unrecorded_since', {
                          since: dayjs(u.since).utcOffset(8 * 60).format('YYYY-MM-DD'),
                          n: u.n,
                          m: u.store_failed_30d,
                        })}{' '}
                        <Link to="/manage/audit?action=predictions.store_failed">
                          {t('forecast.cov.unrecorded_audit')}
                        </Link>
                      </>
                    }
                  />
                ))}
            </div>
          )}
        </Card>
        </div>
        </SettingsPanel>
        <SettingsPanel section="diagnostics">{settings && <ForecastDiagnostics value={settings.diagnostics} fallback={settings.fallback} />}</SettingsPanel>
        <SettingsPanel section="alerts">{settings && <ForecastAlerts settings={settings} onChanged={load} />}</SettingsPanel>
        <SettingsPanel section="access">
        <ForecastAccessSummary />
        </SettingsPanel>
        </SettingsWorkspace>
      </div>

      {settings && (
        <ForecastSettingsModal
          open={settingsOpen}
          value={settings}
          onClose={() => setSettingsOpen(false)}
          onSaved={load}
        />
      )}

      {settings && (
        <ForecastLedgerStart
          open={ledgerOpen}
          value={settings.population}
          onClose={() => setLedgerOpen(false)}
          onSaved={load}
        />
      )}
    </LoadGate>
  )
}
