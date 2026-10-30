import { useEffect } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { closeJobDetail, useOpenJobDetail } from '../lib/jobDetailStore'
import JobDetailDrawer from './JobDetailDrawer'

// The one in-place run detail of the signed-in shell: opened from a Tasks drawer row or a notification,
// drawn over whatever is on screen (including the Tasks drawer), never navigating by itself.
// "Open in queue" is the way to the full page. Any route change (a "View report" link inside the detail,
// the browser's back button) puts the detail and, through `onNavigate`, the shell's Tasks drawer away,
// so neither is left open over a page they have nothing to do with.
export default function JobDetailHost({ onNavigate }: { onNavigate?: () => void }) {
  const id = useOpenJobDetail()
  const navigate = useNavigate()
  const loc = useLocation()
  const here = loc.pathname + loc.search
  useEffect(() => {
    closeJobDetail()
    onNavigate?.()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [here])
  return (
    <JobDetailDrawer
      jobId={id}
      zIndex={1010}
      onClose={closeJobDetail}
      onOpenInQueue={() => {
        if (id == null) return
        const to = `/queue?job=${id}`
        if (here === to) {
          // Already there: the page shows this run; just put the global overlays away.
          closeJobDetail()
          onNavigate?.()
        } else navigate(to)
      }}
    />
  )
}
