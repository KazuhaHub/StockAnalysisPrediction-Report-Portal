// Announcement variables: placeholders an operator writes into an announcement's title or body,
// rendered here, in the reader's browser, because only the browser knows the reader's clock and
// zone. The stored text is the template; the server validates it and never renders it.
//
// The grammar is mirrored exactly by internal/app/announcement_vars.go — change both together, and
// the shared case list in both test files with them:
//
//   {{ name [":" arg] *("|" option) }}
//   now        no arg; options: one format (date|time|datetime), one IANA zone
//   time       arg = an RFC3339 instant; options as for now
//   countdown  arg = an RFC3339 instant; no options
//   site       no arg, no options
//
// Every rendered time carries its full IANA zone name, e.g. "2026-11-20 22:00 (Asia/Shanghai)".
// Abbreviations are never used: "CST" is US Central in the zone database and China Standard Time
// in common usage, and the zone database has none at all for most of Asia.

const TOKEN = /\{\{([^{}]*)\}\}/g
const INSTANT = /^[1-9]\d{3}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,9})?)?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)$/
const ZONE = /^(UTC|[A-Z][A-Za-z_-]*(\/[A-Z0-9][A-Za-z0-9_+-]*)+)$/
const FORMATS = new Set(['date', 'time', 'datetime'])

type Format = 'date' | 'time' | 'datetime'

type Parsed =
  | { name: 'site' }
  | { name: 'countdown'; at: Date }
  | { name: 'now' | 'time'; at?: Date; format: Format; zone?: string }

export interface AnnouncementVarContext {
  now: Date
  /** The reader's IANA zone, used when the operator did not pin one. */
  zone: string
  site: string
  /** The UI language, for the countdown's wording. */
  lang: string
}

function parseInstant(s: string): Date | null {
  const m = INSTANT.exec(s)
  if (!m) return null
  // Reject what Date would silently roll over (month 13, day 40): the server's time.Parse refuses
  // those, and the two sides must agree.
  const [y, mo, d, h, mi] = [s.slice(0, 4), s.slice(5, 7), s.slice(8, 10), s.slice(11, 13), s.slice(14, 16)].map(Number)
  const sec = m[1] ? Number(s.slice(17, 19)) : 0
  if (mo < 1 || mo > 12 || h > 23 || mi > 59 || sec > 59) return null
  if (d < 1 || d > new Date(Date.UTC(y, mo, 0)).getUTCDate()) return null
  const t = Date.parse(s)
  return Number.isNaN(t) ? null : new Date(t)
}

function validZone(z: string): boolean {
  if (!ZONE.test(z)) return false
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: z })
    return true
  } catch {
    return false
  }
}

// ASCII spaces and tabs only, matching trimVarSpace on the server: String.prototype.trim and Go's
// strings.TrimSpace disagree on Unicode spaces.
const trimVar = (s: string) => s.replace(/^[ \t]+|[ \t]+$/g, '')

function parseOptions(opts: string[]): { format: Format; zone?: string } | null {
  let format: Format | undefined
  let zone: string | undefined
  for (const raw of opts) {
    const o = trimVar(raw)
    if (FORMATS.has(o)) {
      if (format) return null
      format = o as Format
    } else if (validZone(o)) {
      if (zone) return null
      zone = o
    } else {
      return null
    }
  }
  return { format: format ?? 'datetime', zone }
}

function parse(body: string): Parsed | null {
  const [head, ...opts] = body.split('|')
  const colon = head.indexOf(':')
  const hasArg = colon >= 0
  const name = trimVar(hasArg ? head.slice(0, colon) : head)
  const arg = hasArg ? trimVar(head.slice(colon + 1)) : ''
  switch (name) {
    case 'site':
      return !hasArg && opts.length === 0 ? { name } : null
    case 'countdown': {
      const at = hasArg ? parseInstant(arg) : null
      return at && opts.length === 0 ? { name, at } : null
    }
    case 'now': {
      if (hasArg) return null
      const o = parseOptions(opts)
      return o ? { name, ...o } : null
    }
    case 'time': {
      const at = hasArg ? parseInstant(arg) : null
      const o = at && parseOptions(opts)
      return at && o ? { name, at, ...o } : null
    }
  }
  return null
}

/**
 * The first placeholder in s that is not a valid variable, verbatim, or '' when all are valid.
 * The editor uses it to show the operator the exact token the server would refuse.
 */
export function checkAnnouncementVars(s: string): string {
  for (const m of s.matchAll(TOKEN)) {
    if (!parse(m[1])) return m[0]
  }
  return ''
}

/** Whether the text changes as the clock moves, so its renderer has to tick. */
export function hasClockVars(s: string): boolean {
  for (const m of s.matchAll(TOKEN)) {
    const p = parse(m[1])
    if (p && (p.name === 'now' || p.name === 'countdown')) return true
  }
  return false
}

function formatInZone(at: Date, zone: string, format: Format): string {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  }).formatToParts(at)
  const p = (type: string) => parts.find((x) => x.type === type)?.value ?? ''
  const date = `${p('year')}-${p('month')}-${p('day')}`
  const time = `${p('hour')}:${p('minute')}`
  const text = format === 'date' ? date : format === 'time' ? time : `${date} ${time}`
  return `${text} (${zone})`
}

function countdown(at: Date, now: Date, lang: string): string {
  const minutes = Math.round((at.getTime() - now.getTime()) / 60000)
  const abs = Math.abs(minutes)
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' })
  if (abs < 120) return rtf.format(minutes, 'minute')
  if (abs < 48 * 60) return rtf.format(Math.round(minutes / 60), 'hour')
  return rtf.format(Math.round(minutes / 1440), 'day')
}

/**
 * Renders every valid placeholder in s. An invalid one is left exactly as written: a reader seeing
 * "{{tiem:…}}" is a visible bug somebody reports, where an empty gap is one nobody notices.
 */
export function renderAnnouncementVars(s: string, ctx: AnnouncementVarContext): string {
  if (!s.includes('{{')) return s
  return s.replace(TOKEN, (whole, body: string) => {
    const p = parse(body)
    if (!p) return whole
    switch (p.name) {
      case 'site':
        return ctx.site
      case 'countdown':
        return countdown(p.at, ctx.now, ctx.lang)
      default:
        return formatInZone(p.at ?? ctx.now, p.zone ?? ctx.zone, p.format)
    }
  })
}

/** The reader's IANA zone as the browser reports it. */
export function readerZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}
