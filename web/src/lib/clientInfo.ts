import type { ClientSummary } from '../api/types'

export interface ClientInfo extends ClientSummary {
  user_agent?: string
  client_hints?: Record<string, string>
}

export function clientLabel(client?: ClientSummary, deviceTypeLabel = ''): string {
  if (!client) return ''
  const browser = [client.browser, client.browser_version].filter(Boolean).join(' ')
  const os = [client.os, client.os_version].filter(Boolean).join(' ')
  return [browser, os, client.device || deviceTypeLabel].filter(Boolean).join(' · ')
}

export function clientInfoFromDetail(detail: string): ClientInfo | undefined {
  try {
    const parsed: unknown = JSON.parse(detail)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return undefined
    const client = (parsed as { client?: unknown }).client
    if (!client || typeof client !== 'object' || Array.isArray(client)) return undefined
    return client as ClientInfo
  } catch {
    return undefined
  }
}
