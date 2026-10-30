import { errText } from '../api/client'
import { isConnectionError } from '../api/connection'

/**
 * What a failed data load should show: null when the cause is the connection (the connection strip
 * already says so and the view keeps its previous state), otherwise the user-facing text.
 *
 * Only for loads. A user action (save, run, delete…) answers the click, so it keeps using errText.
 */
export function loadFailure(
  e: unknown,
  t: (k: string, o?: Record<string, unknown>) => string,
  fallbackKey?: string,
): string | null {
  return isConnectionError(e) ? null : errText(e, t, fallbackKey)
}

/** `.catch(onLoadFailure(t, setLoadErr))` for a load nobody needs to retry by hand (a poll runs it again). */
export function onLoadFailure(t: (k: string, o?: Record<string, unknown>) => string, report: (text: string) => void, fallbackKey?: string) {
  return (e: unknown) => {
    const text = loadFailure(e, t, fallbackKey)
    if (text !== null) report(text)
  }
}

/**
 * The two arguments of a `.then(done, onFail)` that settles a loading flag the way `.finally` would,
 * except when the cause is the connection: that is not an answer, so the flag stays up (the view
 * keeps its spinner) and the rejection continues to the caller's catch.
 */
export function settleOnAnswer(done: () => void): [() => void, (e: unknown) => never] {
  return [done, (e) => { if (!isConnectionError(e)) done(); throw e }]
}
