import { waitFor } from '@testing-library/react'
import { getNotifications, type NotificationItem } from '../lib/notify'

// NotificationStack is mounted by AppLayout, not by an isolated component test, so a failure raised
// through notifyError is asserted on the notification store instead of on the DOM. The global
// teardown (test/setup.ts) resets the store after every test.
export async function findFailure(title: string | RegExp): Promise<NotificationItem> {
  let found: NotificationItem | undefined
  await waitFor(() => {
    found = getNotifications().find(
      (n) => n.kind === 'failure' && typeof n.title === 'string' && (typeof title === 'string' ? n.title === title : title.test(n.title)),
    )
    if (!found) throw new Error(`no failure notification matching ${String(title)}`)
  })
  return found as NotificationItem
}
