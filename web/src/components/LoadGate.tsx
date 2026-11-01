import type { ReactNode } from 'react'
import { Button, Result, Spin, Typography } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import WaitingForConnection from './WaitingForConnection'

// What a view shows while the answer it describes is still on the wire.
//
// The alternative — and what several pages did — is to render immediately with the values its
// useState calls happen to start at. On a fast link that reads as a flicker; on a slow one, or
// a failed request, it is a page telling an admin that the queue runs one job at a time and
// mail is switched off, in a form whose Save button will happily write exactly that back. A
// default is not a value the server sent, and must never be presented as one.
//
// The same holds for a list: `[]` is where the state starts, not something the server said, so
// "queue is empty" / "no apps available" rendered off it is a claim nobody made. Such a view
// passes `title` to say what failed to load in its own words instead of the settings wording.
//
// So: a placeholder until the answer is in, and if it never comes, say so and offer the retry —
// never fall through to the defaults.
//
// A connection loss is not an answer either: the caller keeps `loading` true and passes no error,
// the shell's connection strip says the connection is down, and the spinner here only adds a
// muted line that it is waiting.
//
// The placeholder is the shared loading ring, centred in the space the view will fill, with its
// words. It is there from the first frame and fades in, so a fast answer leaves at most a faint
// trace while a slow one is acknowledged at once where the reader is looking. (Holding it back
// behind a delay left a blank frame instead, once per Suspense layer a page passes through; and a
// generic skeleton, one set of grey bars for every page, previewed none of them.)
// The loading status is announced from the first frame.
export default function LoadGate({
  loading,
  error,
  onRetry,
  children,
  minHeight = '40vh',
  title,
}: {
  loading: boolean
  error?: string
  onRetry?: () => void
  children: ReactNode
  minHeight?: number | string
  title?: string
}) {
  const { t } = useTranslation()
  if (error) {
    return (
      <Result
        status="warning"
        title={title ?? t('common.loadFailed')}
        subTitle={error}
        extra={
          onRetry ? (
            <Button icon={<ReloadOutlined />} onClick={onRetry}>
              {t('common.retry')}
            </Button>
          ) : undefined
        }
      />
    )
  }
  if (loading) {
    return <LoadingPlaceholder minHeight={minHeight} />
  }
  return <>{children}</>
}

/**
 * The placeholder LoadGate shows, for a view that loads in its own way: the loading ring after a
 * short delay, the loading status announced from the first frame, and the waiting-for-connection line.
 */
export function LoadingPlaceholder({
  minHeight,
  label,
}: {
  minHeight?: number | string
  label?: string
}) {
  const { t } = useTranslation()
  return (
    <div aria-busy className="rp-loading" style={{ minHeight }}>
      <Spin />
      <Typography.Text role="status" type="secondary">{label ?? t('common.loading')}</Typography.Text>
      <WaitingForConnection />
    </div>
  )
}
