// SemVer 2.0 precedence, the same rule as appplatform.CompareVersions on the server: numeric major.minor.patch,
// a prerelease sorts below its release, prerelease identifiers compare dot by dot (numeric below alphanumeric,
// a shorter list below a longer one with the same prefix), and build metadata is ignored.
const numeric = /^\d+$/

function compareNumeric(a: string, b: string): number {
  const x = a.replace(/^0+/, ''), y = b.replace(/^0+/, '')
  if (x.length !== y.length) return x.length < y.length ? -1 : 1
  return x < y ? -1 : x > y ? 1 : 0
}

function split(version: string): { core: string[]; pre: string[] | null } {
  const [withoutBuild] = version.split('+', 1)
  const dash = withoutBuild.indexOf('-')
  const core = (dash < 0 ? withoutBuild : withoutBuild.slice(0, dash)).split('.')
  while (core.length < 3) core.push('0')
  return { core, pre: dash < 0 ? null : withoutBuild.slice(dash + 1).split('.') }
}

/** -1 when `a` is older than `b`, 0 when they have the same precedence, 1 when `a` is newer. */
export function compareVersions(a: string, b: string): number {
  const x = split(a), y = split(b)
  for (let i = 0; i < 3; i++) {
    const c = compareNumeric(x.core[i], y.core[i])
    if (c) return c
  }
  if (!x.pre && !y.pre) return 0
  if (!x.pre) return 1
  if (!y.pre) return -1
  for (let i = 0; i < Math.min(x.pre.length, y.pre.length); i++) {
    const p = x.pre[i], q = y.pre[i]
    const pn = numeric.test(p), qn = numeric.test(q)
    const c = pn && qn ? compareNumeric(p, q) : pn ? -1 : qn ? 1 : p < q ? -1 : p > q ? 1 : 0
    if (c) return c
  }
  return x.pre.length < y.pre.length ? -1 : x.pre.length > y.pre.length ? 1 : 0
}

/** True for a version with a prerelease part (`1.0.0-beta.1`). */
export const isPrerelease = (version: string): boolean => version.split('+', 1)[0].includes('-')

/**
 * The version a catalogue offers by default, the way Docker's `latest` works: the newest stable version when
 * one exists, otherwise the newest prerelease. The order of `versions` does not matter.
 */
export function latestVersion(versions: readonly string[]): string | undefined {
  const newest = (list: string[]) => list.reduce<string | undefined>((best, v) => (best === undefined || compareVersions(v, best) > 0 ? v : best), undefined)
  return newest(versions.filter(v => !isPrerelease(v))) ?? newest([...versions])
}

/**
 * The version an installed app should be offered as an update, if any: the catalogue's latest when it is newer
 * than the installed one and either stable or the installed version is itself a prerelease. A stable install is
 * never nudged toward a prerelease.
 */
export function offeredUpdate(installed: string, versions: readonly string[]): string | undefined {
  const latest = latestVersion(versions)
  if (!latest || compareVersions(latest, installed) <= 0) return undefined
  return !isPrerelease(latest) || isPrerelease(installed) ? latest : undefined
}
