import { useEffect, useState } from 'react'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import { formatElapsed } from '../lib/taskUi'

// "Running · 2m 13s": the time since a run started, ticking each second while the page is visible.
// A wall-clock start (what the run queue stores) is read in the browser's zone, like the row times.
export default function RunningFor({ since }: { since: string }) {
  const { t } = useTranslation()
  const [now, setNow] = useState(() => Date.now())
  const start = since ? dayjs(since) : null
  const valid = !!start && start.isValid()
  useEffect(() => {
    if (!valid) return
    setNow(Date.now())
    const id = setInterval(() => {
      if (document.visibilityState !== 'hidden') setNow(Date.now())
    }, 1000)
    return () => clearInterval(id)
  }, [valid])
  if (!valid) return <>{t('batch.status.running')}</>
  return <>{t('tasks.runningFor', { elapsed: formatElapsed(now - start.valueOf()) })}</>
}
