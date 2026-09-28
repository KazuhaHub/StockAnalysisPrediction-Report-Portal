import { describe, it, expect } from 'vitest'
import { countryFlag, countryName, formatRegion } from './geo'

describe('geo labels', () => {
  it('turns an ISO code into a flag', () => {
    expect(countryFlag('CN')).toBe('🇨🇳')
    expect(countryFlag('us')).toBe('🇺🇸')
  })

  // A bad code must render as nothing, not as two stray letter-glyphs.
  it('refuses anything that is not two letters', () => {
    for (const bad of ['', 'C', 'CHN', '12', undefined]) {
      expect(countryFlag(bad)).toBe('')
    }
  })

  it('localizes the country code for the selected interface language', () => {
    expect(countryName({ country: 'China', country_code: 'CN' }, 'zh-CN')).toBe('中国')
    expect(countryName({ country_code: 'JP' }, 'en')).toBe('Japan')
    expect(countryName({})).toBe('')
  })

  it('uses localized database names for subdivisions and cities', () => {
    const geo = {
      country_code: 'US', country: 'United States', region: 'California', city: 'Los Angeles',
      localized_names: {
        country: { en: 'United States', 'zh-CN': '美国' },
        region: { en: 'California', 'zh-CN': '加利福尼亚州' },
        city: { en: 'Los Angeles', 'zh-CN': '洛杉矶' },
      },
    }
    expect(formatRegion(geo, 'zh-CN')).toBe('🇺🇸 美国 · 加利福尼亚州 · 洛杉矶')
    expect(formatRegion(geo, 'en')).toBe('🇺🇸 United States · California · Los Angeles')
  })

  it('builds country · region · city', () => {
    expect(formatRegion({ country_code: 'CN', country: 'China', region: 'Guangdong', city: 'Shenzhen' }, 'en')).toBe(
      '🇨🇳 China · Guangdong · Shenzhen',
    )
  })

  // Free databases report both for a municipality; "Shanghai · Shanghai" reads as a bug.
  it('drops a city that repeats its region', () => {
    expect(formatRegion({ country_code: 'CN', country: 'China', region: 'Shanghai', city: 'Shanghai' }, 'en')).toBe(
      '🇨🇳 China · Shanghai',
    )
  })

  it('renders a country-only result, and nothing at all for an unknown address', () => {
    expect(formatRegion({ country_code: 'CN', country: 'China' }, 'en')).toBe('🇨🇳 China')
    expect(formatRegion({})).toBe('')
    expect(formatRegion(undefined)).toBe('')
  })
})
