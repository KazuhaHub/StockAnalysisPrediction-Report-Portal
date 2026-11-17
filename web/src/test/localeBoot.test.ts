import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { detectLang, normalizeSaved, LANGS } from '../locales'

// public/locale-boot.js runs before the app bundle (the CSP forbids inline scripts, so it is a
// same-origin file). index.html ships the base language's name in the tab title and on the boot
// splash; without this script an English or Traditional Chinese reader saw 研报门户 until React
// mounted. It must pick the language exactly as prefs.tsx does, and must never touch a site title
// the administrator configured (the server injects that into the HTML before it is served).
const web = resolve(__dirname, '../..')
const script = readFileSync(resolve(web, 'public/locale-boot.js'), 'utf8')
const indexHtml = readFileSync(resolve(web, 'index.html'), 'utf8')
const brands: Record<string, string> = Object.fromEntries(
  LANGS.map((l) => [l.code, JSON.parse(readFileSync(resolve(web, `src/locales/${l.code}.json`), 'utf8')).brand]),
)
const DEFAULT_TITLE = '研报门户'
const realLanguages = Object.getOwnPropertyDescriptor(window.navigator, 'languages')

function boot(opts: { stored?: string | null; languages?: string[]; title?: string; storageThrows?: boolean }) {
  window.localStorage.clear()
  if (opts.stored != null) window.localStorage.setItem('rp_locale', opts.stored)
  Object.defineProperty(window.navigator, 'languages', { configurable: true, value: opts.languages ?? [] })
  const title = opts.title ?? DEFAULT_TITLE
  document.title = title
  document.documentElement.lang = 'zh-CN'
  document.body.innerHTML = `<div id="boot-splash"><div id="boot-name">${title}</div></div>`
  const getItem = Storage.prototype.getItem
  if (opts.storageThrows) {
    Storage.prototype.getItem = () => {
      throw new Error('blocked')
    }
  }
  try {
    new Function(script)()
  } finally {
    Storage.prototype.getItem = getItem
  }
  return {
    lang: document.documentElement.lang,
    title: document.title,
    bootName: document.getElementById('boot-name')?.textContent,
  }
}

beforeEach(() => {
  document.body.innerHTML = ''
})

afterEach(() => {
  if (realLanguages) Object.defineProperty(window.navigator, 'languages', realLanguages)
  else Reflect.deleteProperty(window.navigator, 'languages')
})

describe('locale-boot.js', () => {
  // The same inputs through the app's own resolver: if prefs.tsx changes how it picks a language,
  // this fails instead of the boot screen silently disagreeing with the app that replaces it.
  const cases: Array<{ stored: string | null; languages: string[] }> = [
    { stored: 'en-US', languages: ['zh-CN'] },
    { stored: 'zh-TW', languages: ['en-US'] },
    { stored: 'zh-CN', languages: ['en-US'] },
    { stored: 'en', languages: [] },
    { stored: 'zh', languages: [] },
    { stored: 'fr-FR', languages: ['en-US'] },
    { stored: null, languages: ['en-GB', 'zh-CN'] },
    { stored: null, languages: ['zh-HK'] },
    { stored: null, languages: ['zh-Hant-CN'] },
    { stored: null, languages: ['zh-Hans-TW'] },
    { stored: null, languages: ['zh'] },
    { stored: null, languages: ['fr-FR', 'de-DE'] },
    { stored: null, languages: [] },
  ]
  for (const c of cases) {
    it(`picks the app's language for stored=${c.stored} languages=${c.languages.join(',') || '-'}`, () => {
      const want = c.stored ? normalizeSaved(c.stored) : detectLang(c.languages)
      expect(boot(c)).toEqual({ lang: want, title: brands[want], bootName: brands[want] })
    })
  }

  it('leaves a configured site title alone, on the tab and on the splash', () => {
    expect(boot({ stored: 'en-US', title: 'Acme Research' })).toEqual({
      lang: 'en-US',
      title: 'Acme Research',
      bootName: 'Acme Research',
    })
  })

  it('detects from the browser when storage is unavailable', () => {
    expect(boot({ stored: 'zh-TW', languages: ['en-US'], storageThrows: true }).lang).toBe('en-US')
  })
})

describe('index.html loads locale-boot.js', () => {
  it('after the splash it renames and before the app bundle, as a plain same-origin script', () => {
    const tag = indexHtml.indexOf('<script src="/locale-boot.js"></script>')
    expect(tag).toBeGreaterThan(indexHtml.indexOf('id="boot-name"'))
    expect(tag).toBeLessThan(indexHtml.indexOf('src="/src/main.tsx"'))
  })

  it('still ships the default title the script and the server both key on', () => {
    expect(indexHtml).toContain(`<title>${DEFAULT_TITLE}</title>`)
    expect(brands['zh-CN']).toBe(DEFAULT_TITLE)
  })
})
