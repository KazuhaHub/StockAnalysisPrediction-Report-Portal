import { Skeleton } from 'antd'

/**
 * A placeholder of stable size for a name or label whose data is still on its way: shown instead of the identifier it
 * would otherwise fall back to, so the page never flashes an id before the real name arrives. The identifier is shown
 * only after the data has loaded and genuinely carries no name. Its shimmer stops under reduced motion (index.css).
 */
export default function PendingText({ width = 140, height = 16, label }: { width?: number; height?: number; label: string }) {
  return <span className="rp-pending-text" role="status" aria-label={label} style={{ display: 'inline-block', width, height, maxWidth: '100%', verticalAlign: 'middle' }}>
    <Skeleton.Input active size="small" style={{ width, minWidth: 0, height, maxWidth: '100%' }} />
  </span>
}
