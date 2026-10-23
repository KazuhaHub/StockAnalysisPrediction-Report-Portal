/// <reference types="node" />
import { expect, it } from 'vitest'
import { execFileSync } from 'node:child_process'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

it('prefix regeneration preserves unrelated translations and is byte-stable on a second pass', () => {
  const dir = mkdtempSync(join(tmpdir(), 'forecast-locale-'))
  try {
    mkdirSync(join(dir, 'scripts'))
    mkdirSync(join(dir, 'src/locales'), { recursive: true })
    copyFileSync(resolve('scripts/gen-zh-tw.mjs'), join(dir, 'scripts/gen-zh-tw.mjs'))
    symlinkSync(resolve('node_modules'), join(dir, 'node_modules'), 'dir')
    writeFileSync(join(dir, 'src/locales/zh-CN.json'), JSON.stringify({ 'forecast.title': '验证', 'other.title': '其他' }))
    const target = join(dir, 'src/locales/zh-TW.json')
    writeFileSync(target, JSON.stringify({ 'forecast.title': 'old', 'forecast.deleted': 'old', 'other.title': 'reviewed', 'other.only': 'keep' }))
    const run = () => execFileSync(process.execPath, [join(dir, 'scripts/gen-zh-tw.mjs'), '--prefix=forecast.'])
    run()
    const first = readFileSync(target, 'utf8')
    expect(JSON.parse(first)).toEqual({ 'forecast.title': '驗證', 'other.title': 'reviewed', 'other.only': 'keep' })
    run()
    expect(readFileSync(target, 'utf8')).toBe(first)
    expect(() => execFileSync(process.execPath, [join(dir, 'scripts/gen-zh-tw.mjs'), '--prefix='], { stdio: 'pipe' })).toThrow()
    expect(readFileSync(target, 'utf8')).toBe(first)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})
