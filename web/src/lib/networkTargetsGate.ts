// Pacing of an app's requests for network destinations, kept per installation at module level so
// it survives iframe reloads: an app cannot reset it with location.reload(). The bridge session has
// its own short back-off, but that session is rebuilt on every load.
const BASE_MS = 10_000
const MAX_MS = 5 * 60_000

interface Entry { until: number; refusals: number }
const entries = new Map<string, Entry>()
const open = new Set<string>()

export function networkTargetsGate(installation: string): 'ok' | 'busy' | 'backoff' {
  if (open.has(installation)) return 'busy'
  const entry = entries.get(installation)
  return entry && Date.now() < entry.until ? 'backoff' : 'ok'
}

export function networkTargetsOpen(installation: string): () => void {
  open.add(installation)
  return () => { open.delete(installation) }
}

// Any outcome other than applied counts, so that a request that fails before a dialog
// (no settings, a refused plan) cannot be repeated at the rate of a page reload either. Repeated
// refusals double the wait, up to five minutes.
export function noteNetworkTargetsRefusal(installation: string) {
  const refusals = Math.min((entries.get(installation)?.refusals ?? 0) + 1, 16)
  entries.set(installation, { refusals, until: Date.now() + Math.min(BASE_MS * 2 ** (refusals - 1), MAX_MS) })
}

export function noteNetworkTargetsApplied(installation: string) {
  entries.delete(installation)
}

export function resetNetworkTargetsGate() {
  entries.clear()
  open.clear()
}
