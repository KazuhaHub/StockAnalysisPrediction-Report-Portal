import type { ReactNode } from 'react'
import { Alert, Button, Flex, Typography } from 'antd'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { PageActionSnapshot } from '../../lib/page-v2'
import { useNarrow } from '../../lib/useNarrow'

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
  const narrow = useNarrow()
  const title = state?.page.title || name
  const back = onBack && <Button icon={<ArrowLeftOutlined />} onClick={onBack}>{t('apps.back')}</Button>
  const heading = (full: boolean) => <Flex gap={12} align="center" style={full ? { minWidth: 0, width: '100%' } : { minWidth: 0, flex: '1 1 200px' }}>
    {icon}
    <div style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>{title}</Typography.Title>
      {title !== name && <Typography.Text type="secondary">{name}</Typography.Text>}
      {state?.page.description && <div><Typography.Text type="secondary">{state.page.description}</Typography.Text></div>}
    </div>
  </Flex>
  const actions = (state || extra) && <Flex gap={8} wrap style={{ minWidth: 0, maxWidth: '100%' }}>
    {extra}
    {state?.page.actions.map(action => <Button key={action.id}
      style={{ maxWidth: '100%', minWidth: 0, height: 'auto', minHeight: 32, whiteSpace: 'normal', overflowWrap: 'anywhere' }}
      aria-label={action.label} aria-busy={action.loading || state.busyAction === action.id}
      type={action.variant === 'primary' ? 'primary' : 'default'} danger={action.variant === 'danger'}
      loading={action.loading || state.busyAction === action.id}
      disabled={!state.ready || action.disabled || action.loading || state.busyAction !== null}
      onClick={() => state.invoke(action.id)}>{action.label}</Button>)}
  </Flex>
  return <div>
    {narrow
      // On a phone the header stacks: Back on its own row, then the title at full width, then the actions,
      // so the title and description are never squeezed into the column left beside Back.
      ? <Flex vertical gap={12} align="flex-start" data-testid="app-page-header-stacked">
        {back}
        {heading(true)}
        {actions}
      </Flex>
      // Wider screens keep one row: Back leads the title, and the icon and title wrap below it as one group
      // rather than squeezing the title to a word per line.
      : <Flex gap={16} align="center" justify="space-between" wrap>
        <Flex gap={12} align="center" wrap style={{ minWidth: 0, flex: '1 1 240px' }}>
          {back}
          {heading(false)}
        </Flex>
        {actions}
      </Flex>}
    {state?.outcome === 'uncertain' && <Alert type="warning" showIcon title={t('apps.v2ActionUncertain')} />}
  </div>
}
