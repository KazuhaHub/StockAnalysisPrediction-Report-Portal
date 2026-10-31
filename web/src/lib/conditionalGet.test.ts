import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { UNCHANGED, forgetTags, getIfChanged } from './conditionalGet'

const respond = (status: number, body: string, etag?: string) =>
  ({
    ok: status < 400,
    status,
    statusText: '',
    text: () => Promise.resolve(body),
    headers: { get: (k: string) => (k === 'ETag' && etag ? etag : null) },
  }) as unknown as Response

describe('getIfChanged', () => {
  beforeEach(() => forgetTags())
  afterEach(() => vi.unstubAllGlobals())

  it('sends back the tag it was given, and reports an unchanged answer as such', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(respond(200, '{"n":1}', 'W/"abc"')).mockResolvedValueOnce(respond(304, ''))
    vi.stubGlobal('fetch', fetchMock)

    expect(await getIfChanged('/api/q')).toEqual({ n: 1 })
    expect(fetchMock.mock.calls[0][1].headers).toBeUndefined() // nothing to revalidate against yet

    expect(await getIfChanged('/api/q')).toBe(UNCHANGED)
    expect(fetchMock.mock.calls[1][1].headers['If-None-Match']).toBe('W/"abc"')
  })

  it('forgets the tag when a request fails, so the next poll asks properly', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(respond(200, '{"n":1}', 'W/"abc"'))
      .mockResolvedValueOnce(respond(500, '{"error":"boom"}'))
      .mockResolvedValueOnce(respond(200, '{"n":2}', 'W/"def"'))
    vi.stubGlobal('fetch', fetchMock)

    await getIfChanged('/api/q')
    await expect(getIfChanged('/api/q')).rejects.toThrow('boom')
    await getIfChanged('/api/q')
    expect(fetchMock.mock.calls[2][1].headers).toBeUndefined()
  })

  it('drops the tag when a response arrives without one, rather than revalidating against a stale key', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(respond(200, '{"n":1}', 'W/"abc"'))
      .mockResolvedValueOnce(respond(200, '{"n":2}'))
      .mockResolvedValueOnce(respond(200, '{"n":3}'))
    vi.stubGlobal('fetch', fetchMock)

    await getIfChanged('/api/q')
    await getIfChanged('/api/q')
    await getIfChanged('/api/q')
    expect(fetchMock.mock.calls[2][1].headers).toBeUndefined()
  })

  // Several pollers ask for one URL (the queue summary: the header badge, the Tasks drawer, the
  // queue page). A tag moved on by one of them must not let another hear "unchanged" about an
  // answer it never had.
  it('keeps each holder to its own tag, so one poller cannot make another stale', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(respond(200, '{"n":1}', 'W/"a1"'))
      .mockResolvedValueOnce(respond(200, '{"n":2}', 'W/"a2"'))
      .mockResolvedValueOnce(respond(304, ''))
    vi.stubGlobal('fetch', fetchMock)
    const badge = {}
    const drawer = {}

    await getIfChanged('/api/q', drawer)
    await getIfChanged('/api/q', badge)
    await getIfChanged('/api/q', drawer)
    expect(fetchMock.mock.calls[1][1].headers).toBeUndefined()
    expect(fetchMock.mock.calls[2][1].headers['If-None-Match']).toBe('W/"a1"')
  })

  it("forgets one URL for every holder, or every URL for one holder", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(respond(200, '{}', 'W/"t"')))
    vi.stubGlobal('fetch', fetchMock)
    const a = {}
    const b = {}
    await getIfChanged('/api/q', a)
    await getIfChanged('/api/q', b)
    await getIfChanged('/api/r', a)

    forgetTags('/api/q')
    await getIfChanged('/api/q', a)
    await getIfChanged('/api/q', b)
    expect(fetchMock.mock.calls[3][1].headers).toBeUndefined()
    expect(fetchMock.mock.calls[4][1].headers).toBeUndefined()

    forgetTags(undefined, a)
    await getIfChanged('/api/r', a)
    await getIfChanged('/api/q', b)
    expect(fetchMock.mock.calls[5][1].headers).toBeUndefined()
    expect(fetchMock.mock.calls[6][1].headers['If-None-Match']).toBe('W/"t"')
  })
})
