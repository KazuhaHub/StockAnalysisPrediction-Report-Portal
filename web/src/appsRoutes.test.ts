import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { matchRoutes } from 'react-router'

const source = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'App.tsx'), 'utf8')

describe('apps routes', () => {
  it('declares the install page before the app page, so "install" is never read as an app key', () => {
    const install = source.indexOf('path="apps/install"')
    const detail = source.indexOf('path="apps/:key"')
    expect(install).toBeGreaterThan(-1)
    expect(detail).toBeGreaterThan(install)
    expect(source).toMatch(/path="apps\/install" element=\{<AppsInstallPage \/>\}/)
  })

  it('resolves /apps/install to the install page and any other key to the app page', () => {
    const routes = [
      { path: 'apps', id: 'list' },
      { path: 'apps/install', id: 'install' },
      { path: 'apps/:key', id: 'detail' },
      { path: 'apps/:key/access', id: 'access' },
    ]
    const id = (path: string) => matchRoutes(routes, path)?.[0].route.id
    expect(id('/apps/install')).toBe('install')
    expect(id('/apps/builtin%3Aquotes')).toBe('detail')
    expect(id('/apps/platform%3Aabc/access')).toBe('access')
  })
})
