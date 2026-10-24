import { Alert, Card, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

export type QuoteLedgerHealth = {
  source: string
  lastSuccess: string
  lastError: string
  lastErrorAt: string
  consecutiveFailures: number
}

export type QuoteLedgerState = {
  ledgerChain: string[]
  ledgerPrimary: string
  ledgerShadowPeer?: string
  ledgerHealth?: QuoteLedgerHealth[]
}

export default function QuoteLedgerStatus({ state, sourceLabel, stamp }: {
  state: QuoteLedgerState
  sourceLabel: (name: string) => string
  stamp: (value: string) => string
}) {
  const { t } = useTranslation()
  return (
    <Card title={t('quoteAdmin.ledger')}>
      <Space orientation="vertical" style={{ width: '100%' }}>
        <Typography.Text type="secondary">{t('quoteAdmin.ledgerConfigOnly')}</Typography.Text>
        {state.ledgerPrimary ? (
          <Typography.Text>{t('quoteAdmin.ledgerPrimary', { source: sourceLabel(state.ledgerPrimary) })}</Typography.Text>
        ) : <Alert type="warning" showIcon title={t('quoteAdmin.ledgerNoPrimary')} />}
        <Typography.Text>{t('quoteAdmin.ledgerChain', { sources: state.ledgerChain.map(sourceLabel).join(', ') || '—' })}</Typography.Text>
        <Typography.Text>{t('quoteAdmin.ledgerShadow', { source: state.ledgerShadowPeer ? sourceLabel(state.ledgerShadowPeer) : '—' })}</Typography.Text>
        <Typography.Title level={5}>{t('quoteAdmin.ledgerHealth')}</Typography.Title>
        {!state.ledgerHealth ? <Typography.Text>{t('quoteAdmin.ledgerUnavailable')}</Typography.Text> : state.ledgerHealth.length === 0 ? (
          <Typography.Text>{t('quoteAdmin.ledgerNoHistory')}</Typography.Text>
        ) : (
          <ul data-testid="quote-ledger-health">
            {state.ledgerHealth.map((health) => (
              <li key={health.source}>
                <Typography.Text strong>{sourceLabel(health.source)}</Typography.Text>
                <div>{t('quoteAdmin.lastSuccess')}: {stamp(health.lastSuccess) || t('quoteAdmin.never')}</div>
                <div>{t('quoteAdmin.failures')}: {health.consecutiveFailures}</div>
                {health.lastError ? <div><Typography.Text type="danger">{health.lastError}</Typography.Text> {stamp(health.lastErrorAt)}</div> : null}
              </li>
            ))}
          </ul>
        )}
      </Space>
    </Card>
  )
}
