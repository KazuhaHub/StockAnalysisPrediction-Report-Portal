import { api, qs } from './client'

export type NotificationAdapter = 'inbox' | 'email' | 'slack' | 'webhook'
export type NotificationState = 'pending' | 'leased' | 'retry_wait' | 'accepted' | 'failed' | 'uncertain' | 'canceled' | 'expired'

export interface NotificationChannel {
  id: string
  label: string
  adapter: NotificationAdapter
  legacy_ref: string
  enabled: boolean
  paused: boolean
  revision: number
  recipients: string[]
  url_host: string
  has_url: boolean
  has_signing_secret: boolean
  available: boolean
  created_at: string
}

export interface NotificationRoute {
  id: string
  label: string
  channel_id: string
  events: string
  recipient_mode: 'grant' | 'creator' | 'destination'
  enabled: boolean
  mandatory: boolean
  revision: number
  grants: string[]
}

export interface NotificationOverview {
  channels: NotificationChannel[]
  routes: NotificationRoute[]
  events: string[]
  principals: Array<{ principal: string; label: string; kind: 'user' | 'group' }>
  dispatch_paused: boolean
  review_token: string
  counts: Record<'pending' | 'leased' | 'retry_wait' | 'uncertain', number>
  retention: { enabled: boolean; history_days: number; inbox_days: number }
}

export interface NotificationChannelInput {
  label: string
  adapter?: NotificationAdapter
  revision?: number
  recipients?: string[]
  url?: string
  signing_secret?: string
  clear_destination?: boolean
  clear_signing_secret?: boolean
  enabled?: boolean
  paused?: boolean
}

export interface NotificationRouteInput {
  label: string
  channel_id: string
  events: string[]
  recipient_mode: NotificationRoute['recipient_mode']
  enabled: boolean
  mandatory: boolean
  grants: string[]
  revision?: number
}

export interface NotificationDelivery {
  id: string
  event_id: string
  event_type: string
  channel_id: string
  route_id: string
  username: string
  state: NotificationState
  attempts: number
  next_at: string
  expires_at: string
  created_at: string
  updated_at: string
  occurred_at: string
  error_code: string
  status_code: number
  replay_of: string
  generation: number
}

export interface NotificationHistory {
  deliveries: NotificationDelivery[]
  next_cursor: string
}

export interface NotificationDeliveryDetail {
  delivery: NotificationDelivery
  attempts: Array<{ id: string; started_at: string; finished_at: string; outcome: string; error_code: string; status_code: number }>
  preview: { subject: string; text: string }
}

const root = '/api/admin/notifications'
const encoded = encodeURIComponent

export const notifications = {
  retention: (input: NotificationOverview['retention']) => api.put(`${root}/retention`, input),
  overview: () => api.get<NotificationOverview>(root),
  saveChannel: (input: NotificationChannelInput, id?: string) => id
    ? api.put<{ ok: boolean; channel: NotificationChannel }>(`${root}/channels/${encoded(id)}`, input)
    : api.post<{ ok: boolean; channel: NotificationChannel }>(`${root}/channels`, input),
  deleteChannel: (id: string, revision: number) => api.del(`${root}/channels/${encoded(id)}`, { revision }),
  saveRoute: (input: NotificationRouteInput, id?: string) => id
    ? api.put<{ ok: boolean; route: NotificationRoute }>(`${root}/routes/${encoded(id)}`, input)
    : api.post<{ ok: boolean; route: NotificationRoute }>(`${root}/routes`, input),
  deleteRoute: (id: string, revision: number) => api.del(`${root}/routes/${encoded(id)}`, { revision }),
  history: (filter: { state?: NotificationState; channel_id?: string; event_type?: string; cursor?: string; limit?: number } = {}) => api.get<NotificationHistory>(`${root}/deliveries${qs(filter)}`),
  delivery: (id: string) => api.get<NotificationDeliveryDetail>(`${root}/deliveries/${encoded(id)}`),
  replay: (id: string, input: { request_id: string; channel_revision: number; route_revision: number; confirm_duplicate: boolean; reason: string }) => api.post<{ ok: boolean; id: string; created: boolean }>(`${root}/deliveries/${encoded(id)}/replay`, input),
  review: (id: string, input: { decision: 'dismiss'; reason: string; updated_at: string }) => api.post(`${root}/deliveries/${encoded(id)}/review`, input),
  dispatch: (paused: boolean, review_token?: string) => api.post<{ ok: boolean; dispatch_paused: boolean; review_token: string; counts: NotificationOverview['counts'] }>(`${root}/dispatch`, { paused, review_token }),
  preview: (input: { event_type?: string; event_id?: string }) => api.post<{ subject: string; text: string; targets: Array<{ channel_id: string; route_id: string; username: string }>; inbox_recipients: string[]; sample: boolean }>(`${root}/preview`, input),
  test: (id: string, input: { revision: number; event_type?: string; to?: string }) => api.post<{ ok: boolean; id: string; state: NotificationState; error_code: string; status_code: number }>(`${root}/channels/${encoded(id)}/test`, input),
}

export interface InboxItem {
  id: string
  event_id: string
  event_type: string
  subject: string
  text: string
  occurred_at: string
  read_at: string
}
export const notificationInbox = {
  list: (cursor?: string) => api.get<{ items: InboxItem[]; unread: number; next_cursor: string }>(`/api/notifications${qs({ cursor })}`),
  unread: () => api.get<{ unread: number }>('/api/notifications/unread'),
  update: (id: string, action: 'read' | 'dismiss') => api.post(`/api/notifications/${encoded(id)}`, { action }),
  snapshot: () => api.post<{ token: string; count: number }>('/api/notifications/read-snapshot'),
  readAll: (token: string) => api.post('/api/notifications/read-all', { token }),
  preferences: () => api.get<{ preferences: Record<string, boolean> }>('/api/notifications/preferences'),
  savePreferences: (preferences: Record<string, boolean>) => api.put('/api/notifications/preferences', { preferences }),
}
