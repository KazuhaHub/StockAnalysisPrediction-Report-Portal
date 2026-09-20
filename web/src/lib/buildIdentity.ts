// Build identity and how one build relates to another.
//
// The portal has two builds that can disagree: the bundle the browser is RUNNING and the binary the
// server is SERVING. They are equal in the ordinary case and diverge the moment a deploy lands under
// an open tab — which is exactly what the update prompt is about.
//
// The running build cannot be asked of the server. The first /api/version answer describes the
// server, and labelling it "the page you are on" would be a claim about a document the browser may
// have loaded hours earlier and, under a service worker, from a cache the server no longer serves.
// So the bundle carries its own identity, inlined by Vite at build time from the same tag, commit
// and date the Go binary is stamped with (release.yml). Comparing the two is then an equality check
// rather than an inference.

export type BuildIdentity = { version: string; commit: string; buildDate: string }

// buildKey is the whole identity as one string: a tag can be re-pushed and a commit can repeat
// across rebuilds, but the trio is unique per artifact. Two builds that differ anywhere differ here.
export function buildKey(b: BuildIdentity): string {
  return `${b.version}@${b.commit}@${b.buildDate}`
}

// pageBuildIdentity is the build THIS bundle was compiled from. A dev server, a plain
// `npm run build` and a test run all compile without the values, and the diagnostic triple is the
// same one the Go binary reports when it is built without -ldflags — the honest answer, since
// inventing a version would let a page claim to be a release it is not.
export function pageBuildIdentity(): BuildIdentity {
  const env = import.meta.env as unknown as Record<string, string | undefined>
  return {
    version: env.VITE_BUILD_VERSION || 'dev',
    commit: env.VITE_BUILD_COMMIT || 'none',
    buildDate: env.VITE_BUILD_DATE || 'unknown',
  }
}

// parseCalVer reads YYYY.W[.R], with or without the git tag's leading v, as a comparable tuple.
// The revision is optional and defaults to 0 — a tag without one is the week's first release, which
// is deliberately a different number from that week's revision 1.
export function parseCalVer(v: string): [number, number, number] | null {
  const m = /^v?(\d{4})\.(\d{1,2})(?:\.(\d{1,3}))?$/.exec(v.trim())
  if (!m) return null
  return [Number(m[1]), Number(m[2]), Number(m[3] ?? 0)]
}

// compareCalVer orders two versions numerically on (year, week, revision), or returns null when
// either is not a CalVer number. Comparison is numeric because comparing the strings would order
// v2026.38 before v2026.9. This only ever decides WORDING — "newer" versus "rollback" — never
// whether a refresh is needed: that is buildKey's job, and a rebuild of the same number needs the
// refresh just as much as a new number does.
export function compareCalVer(a: string, b: string): number | null {
  const pa = parseCalVer(a)
  const pb = parseCalVer(b)
  if (!pa || !pb) return null
  for (let i = 0; i < 3; i += 1) {
    if (pa[i] !== pb[i]) return pa[i] < pb[i] ? -1 : 1
  }
  return 0
}

export type UpdateKind = 'newer' | 'same' | 'rollback'

// classifyChange says how the server's build relates to the page's, or null when they are the same
// build and there is nothing to prompt about. Anything that cannot be ordered — a same-number
// rebuild, a diagnostic version, the retired v0.x line — is "same", because claiming "newer" or
// "rollback" there would be a guess presented as a fact.
export function classifyChange(page: BuildIdentity, target: BuildIdentity): UpdateKind | null {
  if (buildKey(page) === buildKey(target)) return null
  const cmp = compareCalVer(page.version, target.version)
  if (cmp === null || cmp === 0) return 'same'
  return cmp < 0 ? 'newer' : 'rollback'
}

// formatBuildTime renders a build timestamp with the zone it is shown in. The zone is the point:
// the same instant reads as two different dates to a maintainer and a reader, and a bare timestamp
// invites each of them to assume the other's.
export function formatBuildTime(iso: string, locale?: string): string {
  if (!iso.trim()) return iso
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  let zone = ''
  try {
    zone = Intl.DateTimeFormat().resolvedOptions().timeZone
  } catch {
    zone = ''
  }
  const stamp = d.toLocaleString(locale || undefined, { dateStyle: 'medium', timeStyle: 'short' })
  return zone ? `${stamp} (${zone})` : stamp
}
