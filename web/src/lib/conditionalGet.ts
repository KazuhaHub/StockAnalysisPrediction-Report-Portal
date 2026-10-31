import { ApiError, noteSessionLost } from '../api/client'

// A GET that can come back "nothing changed".
//
// The console polls the queue every few seconds for something that changes far less often than it
// asks. The server answers such a poll with 304 and an empty body (writeJSONIfChanged), and this is
// the other half of that arrangement: it remembers the ETag per URL, sends it back, and reports
// UNCHANGED so the caller can keep the object it already has — which is the real win, since an
// unchanged object means no parse, no setState and no re-render.
//
// This is deliberately not folded into api.get. /api/ is Cache-Control: no-store, so the browser's
// own cache does not participate; this revalidation is an explicit arrangement between one poller
// and one endpoint, and a caller that has not been written to handle UNCHANGED must not silently
// receive it.

export const UNCHANGED = Symbol('unchanged')

// A 304 means "unchanged since the tag you sent", so a tag is only worth sending by whoever holds
// the answer it came with. Several pollers ask for the same URL (the queue summary is polled by
// the header badge, the Tasks drawer and the queue page), so each keeps its own tags: a holder is
// any object the poller keeps for as long as it keeps the answers. Callers that pass none share one.
export type TagHolder = object
const SHARED: TagHolder = {}
const tags = new Map<string, Map<TagHolder, string>>()

/**
 * Forget stored tags. With a url, that url's tags for every holder (after a mutation); with a
 * holder only, everything that holder kept (when it lets its answers go); with neither, all of them.
 */
export function forgetTags(url?: string, holder?: TagHolder): void {
  if (url && holder) tags.get(url)?.delete(holder)
  else if (url) tags.delete(url)
  else if (holder) for (const byHolder of tags.values()) byHolder.delete(holder)
  else tags.clear()
}

export async function getIfChanged<T>(url: string, holder: TagHolder = SHARED): Promise<T | typeof UNCHANGED> {
  const known = tags.get(url)?.get(holder)
  const res = await fetch(url, {
    credentials: 'same-origin',
    headers: known ? { 'If-None-Match': known } : undefined,
  })
  if (res.status === 304) return UNCHANGED

  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    // A failed response must not leave a tag behind: the next poll has to ask properly.
    forgetTags(url, holder)
    const body = data as { error?: string; code?: string } | null
    // Same reporting as api.get. This mattered little while the only thing polled through here was
    // the admin queue, which a signed-out reader never reaches; the announcement feed runs on every
    // page for everybody, so a session that ended has to be noticed here too — otherwise the caller
    // swallows the 401 and the app keeps rendering a reader's announcements after their session
    // is gone.
    noteSessionLost(res.status, body?.code)
    throw new ApiError(res.status, body?.error || res.statusText || 'request failed', body?.code)
  }
  const tag = res.headers.get('ETag')
  if (tag) {
    let byHolder = tags.get(url)
    if (!byHolder) tags.set(url, (byHolder = new Map()))
    byHolder.set(holder, tag)
  } else forgetTags(url, holder)
  return data as T
}
