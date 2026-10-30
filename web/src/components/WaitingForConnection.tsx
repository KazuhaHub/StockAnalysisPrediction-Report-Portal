import { useSyncExternalStore } from 'react'
import { Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { getConnectionError, subscribeConnection } from '../api/connection'

/**
 * A muted line shown while the connection is down, for a view that is still waiting on data. The
 * connection strip sits under the top bar and an overlay's mask covers it, so a drawer or modal that
 * keeps its spinner puts this beside it instead. Renders nothing while the connection is up.
 */
export default function WaitingForConnection() {
  const { t } = useTranslation()
  const down = useSyncExternalStore(subscribeConnection, getConnectionError) !== null
  if (!down) return null
  return <Typography.Text type="secondary" role="status" style={{ fontSize: 12, display: 'block', textAlign: 'center' }}>{t('common.waitingForConnection')}</Typography.Text>
}
