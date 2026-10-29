import type { ReactNode } from 'react'
import { Alert, Button, Flex, Space, Typography } from 'antd'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { PageActionSnapshot } from '../../lib/page-v2'

export default function AppPageHeader({ name, icon, state, onBack }: {
  name: string
  icon: ReactNode
  state: PageActionSnapshot | null
  onBack(): void
}) {
  const { t } = useTranslation()
  const title = state?.page.title || name
  return <div>
    <Flex gap={16} align="center" justify="space-between" wrap>
      <Flex gap={12} align="center" style={{ minWidth: 0, flex: '1 1 240px' }}>
        <Button icon={<ArrowLeftOutlined />} onClick={onBack}>{t('apps.back')}</Button>
        {icon}
        <div style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
          <Typography.Title level={4} style={{ margin: 0 }}>{title}</Typography.Title>
          {title !== name && <Typography.Text type="secondary">{name}</Typography.Text>}
          {state?.page.description && <div><Typography.Text type="secondary">{state.page.description}</Typography.Text></div>}
        </div>
      </Flex>
      {state && <Space wrap>
        {state.page.actions.map(action => <Button key={action.id}
          aria-label={action.label} aria-busy={action.loading || state.busyAction === action.id}
          type={action.variant === 'primary' ? 'primary' : 'default'} danger={action.variant === 'danger'}
          loading={action.loading || state.busyAction === action.id}
          disabled={!state.ready || action.disabled || action.loading || state.busyAction !== null}
          onClick={() => state.invoke(action.id)}>{action.label}</Button>)}
      </Space>}
    </Flex>
    {state?.outcome === 'uncertain' && <Alert style={{ marginTop: 12 }} type="warning" showIcon title={t('apps.v2ActionUncertain')} />}
  </div>
}
