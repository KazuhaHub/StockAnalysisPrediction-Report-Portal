import { useNavigate } from 'react-router'
import { closeJobDetail, useOpenJobDetail } from '../lib/jobDetailStore'
import JobDetailDrawer from './JobDetailDrawer'

// The one in-place run detail of the signed-in shell: opened from a Tasks drawer row or a
// notification, drawn over whatever is on screen (including the Tasks drawer), never navigating.
// "Open in queue" is the way to the full page; `onNavigate` lets the shell put its own drawer away first.
export default function JobDetailHost({ onNavigate }: { onNavigate?: () => void }) {
  const id = useOpenJobDetail()
  const navigate = useNavigate()
  return (
    <JobDetailDrawer
      jobId={id}
      zIndex={1010}
      onClose={closeJobDetail}
      onOpenInQueue={() => {
        if (id == null) return
        closeJobDetail()
        onNavigate?.()
        navigate(`/queue?job=${id}`)
      }}
    />
  )
}
