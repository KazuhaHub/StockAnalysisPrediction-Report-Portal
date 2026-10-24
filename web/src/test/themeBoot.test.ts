import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// public/theme-boot.js runs before the app bundle (the CSP forbids inline scripts, so it is a
// same-origin file). It must leave <html> exactly as prefs.tsx would, so the boot splash and native
// scrollbars already match the reader's stored theme on the first paint.
const web = resolve(__dirname, '../..')
const script = readFileSync(resolve(web, 'public/theme-boot.js'), 'utf8')
const indexHtml = readFileSync(resolve(web, 'index.html'), 'utf8')
const realMatchMedia = window.matchMedia

function boot(stored: string | null, systemDark: boolean, storageThrows = false) {
  window.localStorage.clear()
  if (stored !== null) window.localStorage.setItem('rp_theme', stored)
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (q: string) => ({ matches: q.includes('dark') ? systemDark : false, media: q }),
  })
  const getItem = Storage.prototype.getItem
  if (storageThrows) {
    Storage.prototype.getItem = () => {
      throw new Error('blocked')
    }
  }
  try {
    new Function(script)()
  } finally {
    Storage.prototype.getItem = getItem
  }
  return { theme: document.documentElement.dataset.theme, scheme: document.documentElement.style.colorScheme }
}

beforeEach(() => {
  document.documentElement.removeAttribute('data-theme')
  document.documentElement.style.colorScheme = ''
})

afterEach(() => {
  Object.defineProperty(window, 'matchMedia', { configurable: true, value: realMatchMedia })
})

describe('theme-boot.js', () => {
  it('honours a stored light choice on a dark system', () => {
    expect(boot('light', true)).toEqual({ theme: 'light', scheme: 'light' })
  })

  it('honours a stored dark choice on a light system', () => {
    expect(boot('dark', false)).toEqual({ theme: 'dark', scheme: 'dark' })
  })

  it('follows the system for auto or nothing stored', () => {
    expect(boot('auto', true)).toEqual({ theme: 'dark', scheme: 'dark' })
    expect(boot(null, false)).toEqual({ theme: 'light', scheme: 'light' })
  })

  it('falls back to the system when storage is unavailable', () => {
    expect(boot(null, true, true)).toEqual({ theme: 'dark', scheme: 'dark' })
  })
})

describe('index.html', () => {
  it('loads the boot script before the splash, as a plain same-origin script', () => {
    const tag = indexHtml.indexOf('<script src="/theme-boot.js"></script>')
    expect(tag).toBeGreaterThan(-1)
    expect(tag).toBeLessThan(indexHtml.indexOf('id="boot-splash"'))
    expect(indexHtml).not.toMatch(/<script>(?!\s*<\/script>)/)
  })

  it("styles the splash from the stored theme ahead of the system's", () => {
    expect(indexHtml).toMatch(/html\[data-theme='dark'\] #boot-splash\s*\{/)
    expect(indexHtml).toMatch(/html\[data-theme='light'\] #boot-splash\s*\{/)
    expect(indexHtml).toMatch(/html\[data-theme='dark'\] #boot-splash #boot-name\s*\{/)
    expect(indexHtml).toMatch(/html\[data-theme='light'\] #boot-splash #boot-name\s*\{/)
  })
})
