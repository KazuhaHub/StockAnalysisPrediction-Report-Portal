import { expect, it } from 'vitest'
import { dashboardCollision, snapshotDashboardWidget } from './dashboardDrag'

it('targets the pointer rather than the center of a wide overlay, including gaps', () => {
  const rects = new Map([
    ['small', { left: 0, top: 0, width: 180, height: 160, right: 180, bottom: 160 }],
    ['large', { left: 200, top: 0, width: 800, height: 600, right: 1000, bottom: 600 }],
  ])
  const args = { active: { id: 'source' }, droppableRects: rects,
    droppableContainers: [...rects.keys()].map((id) => ({ id })),
    pointerCoordinates: { x: 90, y: 80 },
    collisionRect: { left: 0, top: 0, width: 1000, height: 100, right: 1000, bottom: 100 },
  } as Parameters<typeof dashboardCollision>[0]
  expect(dashboardCollision(args)[0].id).toBe('small')
  args.pointerCoordinates = { x: 190, y: 80 }
  expect(dashboardCollision(args)[0].id).toBe('small')
  args.pointerCoordinates = { x: 600, y: 550 }
  expect(dashboardCollision(args)[0].id).toBe('large')
})

it('freezes the entire mini widget with its original dimensions and removes duplicate identities', () => {
  const source = document.createElement('div')
  source.className = 'rp-dashboard-block rp-dashboard-width-1'
  source.dataset.dashboardBlock = 'shortcut:webhooks'
  source.style.viewTransitionName = 'dashboard-webhooks'
  source.innerHTML = '<button id="move">Move</button><section>Content</section>'
  source.getBoundingClientRect = () => ({ width: 173, height: 245 } as DOMRect)
  const snapshot = snapshotDashboardWidget(source)
  expect(snapshot.width).toBe(173)
  expect(snapshot.height).toBe(245)
  expect(snapshot.node.style.width).toBe('173px')
  expect(snapshot.node.style.height).toBe('245px')
  expect(snapshot.node.querySelector('button')?.textContent).toBe('Move')
  expect(snapshot.node.querySelector('[id]')).toBeNull()
  expect(snapshot.node.hasAttribute('data-dashboard-block')).toBe(false)
  expect(snapshot.node.style.viewTransitionName).toBe('none')
  expect(source.querySelector('#move')).not.toBeNull()
})
