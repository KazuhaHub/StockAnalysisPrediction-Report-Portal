// The connection-loss store. The API client records here when the network or the portal server is
// unreachable; the shell's connection strip, LoadGate and the load paths of every view read it.
// It lives apart from client.ts so a component can read it without importing the whole client.

export const CONNECTION_ERROR_CODES = ['network_unavailable', 'server_unavailable'] as const

let connectionError: string | null = null
const connectionListeners = new Set<() => void>()

export const getConnectionError = () => connectionError

export function subscribeConnection(listener: () => void) {
  connectionListeners.add(listener)
  return () => { connectionListeners.delete(listener) }
}

export function setConnectionError(code: string) {
  connectionError = code
  connectionListeners.forEach(listener => listener())
}

export function clearConnectionError() {
  const recovering = connectionError !== null
  connectionError = null
  connectionListeners.forEach(listener => listener())
  if (recovering && typeof window !== 'undefined') window.dispatchEvent(new Event('rp:connection-restored'))
}

/**
 * True when a failure is the connection being down (the network or the portal server is
 * unreachable) rather than an answer the server gave. A data load that fails this way has not been
 * answered at all: the view keeps what it had and the connection strip is the one place that says
 * so. The check reads the code the API client stamps on the error, so it holds for any ApiError.
 */
export function isConnectionError(e: unknown): boolean {
  if (!(e instanceof Error)) return false
  const code = (e as { code?: unknown }).code
  return typeof code === 'string' && (CONNECTION_ERROR_CODES as readonly string[]).includes(code)
}
