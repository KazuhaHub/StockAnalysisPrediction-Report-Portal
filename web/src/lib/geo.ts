import type { GeoLocation } from '../api/types'

// Rendering a resolved IP, the same way the sibling panel does it.
//
// "🇨🇳 China · Guangdong · Shenzhen" — country, then state/province, then city, each
// shown only when the database supplied it. Free databases are country-accurate and
// city-approximate, so the tail is a hint and the flag plus country is the fact.

/** countryFlag turns a 2-letter ISO code into its flag emoji (a regional-indicator
 *  pair). '' for anything that is not two letters, so a bad code renders as nothing
 *  rather than as two stray glyphs. */
export function countryFlag(cc?: string): string {
  if (!cc || !/^[A-Za-z]{2}$/.test(cc)) return ''
  const base = 0x1f1e6
  const up = cc.toUpperCase()
  return String.fromCodePoint(base + up.charCodeAt(0) - 65, base + up.charCodeAt(1) - 65)
}

/** countryName uses a localized database name when available, then localizes the ISO
 *  code into the selected interface language. The flat database name is the fallback. */
type PlaceNames = Record<string, string> | undefined

function localizedPlace(names: PlaceNames, locale: string | undefined, fallback: string | undefined): string {
  if (!names || !locale) return fallback || ''
  const normalized = locale.replace('_', '-')
  const language = normalized.split('-')[0].toLowerCase()
  const keys = Object.keys(names)
  const find = (candidate: string) => keys.find((key) => key.toLowerCase() === candidate.toLowerCase())
  const candidates = [normalized]
  if (language === 'zh') {
    candidates.push(/-(tw|hk|mo|hant)/i.test(normalized) ? 'zh-TW' : 'zh-CN')
  }
  candidates.push(language)
  for (const candidate of candidates) {
    const key = find(candidate)
    if (key && names[key]) return names[key]
  }
  return fallback || ''
}

export function countryName(g?: GeoLocation, locale = navigator.language): string {
  if (!g) return ''
  const fromDatabase = localizedPlace(g.localized_names?.country, locale, '')
  if (fromDatabase) return fromDatabase
  if (!g.country_code) return g.country || ''
  try {
    const dn = new Intl.DisplayNames([locale, 'en'], { type: 'region' })
    return dn.of(g.country_code.toUpperCase()) || g.country || g.country_code
  } catch {
    return g.country || g.country_code
  }
}

/** formatRegion builds the label. A city equal to its region is dropped, because free
 *  databases routinely report both for a municipality and "Shanghai · Shanghai" reads
 *  as a bug rather than as precision. */
export function formatRegion(g?: GeoLocation, locale = navigator.language): string {
  if (!g || (!g.country_code && !g.country)) return ''
  const region = localizedPlace(g.localized_names?.region, locale, g.region)
  const city = localizedPlace(g.localized_names?.city, locale, g.city)
  const tail: string[] = []
  if (region) tail.push(region)
  if (city && city !== region) tail.push(city)
  const head = [countryFlag(g.country_code), countryName(g, locale)].filter(Boolean).join(' ')
  return tail.length ? `${head} · ${tail.join(' · ')}` : head
}
