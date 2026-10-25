import { describe, expect, it } from 'vitest'
import { ADMIN_PAGES, searchAdminPages } from './adminNavigation'
import en from '../../locales/en-US.json'
import zh from '../../locales/zh-CN.json'

const translate = (bundle: Record<string, string>) => (key: string) => bundle[key] ?? key

describe('administration discovery', () => {
  it('finds a page from a setting topic rather than requiring the page name', () => {
    expect(searchAdminPages('SMTP', translate(en)).map((page) => page.key)).toContain('email')
    expect(searchAdminPages('次票', translate(zh)).map((page) => page.key)).toContain('runqueue')
    expect(searchAdminPages('次票', translate(zh)).map((page) => page.key)).not.toContain('users')
    expect(searchAdminPages('captcha', translate(en)).map((page) => page.key)).toContain('security')
  })

  it('requires every query word and is insensitive to casing and surrounding space', () => {
    expect(searchAdminPages('  SMTP  credentials ', translate(en)).map((page) => page.key)).toEqual(['email'])
    expect(searchAdminPages('SMTP unavailable-topic', translate(en))).toEqual([])
  })

  it('keeps every searchable page and related destination named and discoverable', () => {
    const keys = ADMIN_PAGES.map((page) => page.key)
    expect(new Set(keys).size).toBe(keys.length)
    for (const page of ADMIN_PAGES) {
      expect(en).toHaveProperty(page.label)
      expect(en).toHaveProperty(`manage.description.${page.key}`)
      for (const target of page.related) expect(keys).toContain(target)
    }
  })
})
