import { afterEach, describe, expect, it } from 'vitest'

import { NOTIFICATION_DURATIONS, getNotifications, notifyError, resetNotifications } from './notify'

// A failed action is a failure notification on the shared stack: it waits until it has been seen and
// then stays long enough to read, instead of antd's three-second message that could vanish unseen.
describe('notifyError', () => {
  afterEach(() => resetNotifications())

  it('raises a failure notification', () => {
    notifyError('Could not save')
    const [item] = getNotifications()
    expect(item.kind).toBe('failure')
    expect(item.title).toBe('Could not save')
    expect(NOTIFICATION_DURATIONS.failure).toBeGreaterThanOrEqual(15_000)
  })

  it('shows the same error once rather than stacking it', () => {
    notifyError('Network down')
    notifyError('Network down')
    expect(getNotifications().filter((i) => i.title === 'Network down')).toHaveLength(1)
  })
})
