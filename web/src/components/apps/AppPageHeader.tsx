import type { ReactNode } from 'react'
import { Alert, Button, Flex, Typography } from 'antd'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { PageActionSnapshot } from '../../lib/page-v2'

export default function AppPageHeader({ name, icon, state, onBack, extra }: {
  name: string
  icon: ReactNode
  state: PageActionSnapshot | null
  /** Omitted where the surrounding page already has its own way out. */
  onBack?: () => void
  /** A control that belongs to the page itself, shown with the app's own actions. */
  extra?: ReactNode
}) {
  const { t } = useTranslation()
  const title = state?.page.title || name
  return <div>
    <Flex gap={16} align="center" justify="space-between" wrap>
      <Flex gap={12} align="center" style={{ minWidth: 0, flex: '1 1 240px' }}>
        {onBack && <Button icon={<ArrowLeftOutlined />} onClick={onBack}>{t('apps.back')}</Button>}
        {icon}
        <div style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
          <Typography.Title level={4} style={{ margin: 0 }}>{title}</Typography.Title>
          {title !== name && <Typography.Text type="secondary">{name}</Typography.Text>}
          {state?.page.description && <div><Typography.Text type="secondary">{state.page.description}</Typography.Text></div>}
        </div>
      </Flex>
      {(state || extra) && <Flex gap={8} wrap style={{ minWidth: 0, maxWidth: '100%' }}>
        {extra}
        {state?.page.actions.map(action => <Button key={action.id}
          style={{ maxWidth: '100%', minWidth: 0, height: 'auto', minHeight: 32, whiteSpace: 'normal', overflowWrap: 'anywhere' }}
          aria-label={action.label} aria-busy={action.loading || state.busyAction === action.id}
          type={action.variant === 'primary' ? 'primary' : 'default'} danger={action.variant === 'danger'}
          loading={action.loading || state.busyAction === action.id}
          disabled={!state.ready || action.disabled || action.loading || state.busyAction !== null}
          onClick={() => state.invoke(action.id)}>{action.label}</Button>)}
      </Flex>}
    </Flex>
    {state?.outcome === 'uncertain' && <Alert type="warning" showIcon title={t('apps.v2ActionUncertain')} />}
  </div>
}
