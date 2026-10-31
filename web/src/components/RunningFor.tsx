import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { formatElapsed } from '../lib/taskUi'

// "Running · 2m 13s": the time since a run started, ticking each second while the page is visible.
// `since` is an instant (unix ms): the queue's own wall-clock strings carry no zone and cannot be
// compared with this browser's clock.
export default function RunningFor({ since }: { since: number }) {
  const { t } = useTranslation()
  const [now, setNow] = useState(() => Date.now())
  const valid = since > 0
  useEffect(() => {
    if (!valid) return
    setNow(Date.now())
    const id = setInterval(() => {
      if (document.visibilityState !== 'hidden') setNow(Date.now())
    }, 1000)
    return () => clearInterval(id)
  }, [valid])
  if (!valid) return <>{t('batch.status.running')}</>
  return <>{t('tasks.runningFor', { elapsed: formatElapsed(now - since, t) })}</>
}
