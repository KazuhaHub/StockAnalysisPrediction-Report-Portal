import { closestCenter, pointerWithin, type CollisionDetection } from '@dnd-kit/core'

// A wide overlay can cover several rows. Only the pointer determines its destination.
export const dashboardCollision: CollisionDetection = (args) => {
  if (!args.pointerCoordinates) return closestCenter(args)
  const hits = pointerWithin(args)
  if (hits.length) return hits
  const { x, y } = args.pointerCoordinates
  return closestCenter({ ...args, collisionRect: { left: x, right: x + 1, top: y, bottom: y + 1, width: 1, height: 1 } })
}

export function snapshotDashboardWidget(source: HTMLElement) {
  const { width, height } = source.getBoundingClientRect()
  const node = source.cloneNode(true) as HTMLElement
  for (const element of [node, ...node.querySelectorAll<HTMLElement>('*')]) {
    element.removeAttribute('id')
    element.removeAttribute('data-dashboard-block')
    element.style.viewTransitionName = 'none'
  }
  Object.assign(node.style, { width: `${width}px`, height: `${height}px`, margin: '0', transform: 'none', transition: 'none', opacity: '1', boxSizing: 'border-box' })
  node.classList.remove('rp-dashboard-drop-target')
  node.inert = true
  return { node, width, height }
}
