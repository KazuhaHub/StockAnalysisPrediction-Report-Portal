/// <reference types="node" />
import { expect, it } from 'vitest'
import { createRequire } from 'node:module'
import cn from './zh-CN.json'
import tw from './zh-TW.json'
import en from './en-US.json'

it('keeps the forecast family at the OpenCC Taiwan fixed point pending owner-arranged review', () => {
  const require = createRequire(import.meta.url)
  const convert = require('opencc-js').Converter({ from: 'cn', to: 'twp' })
  for (const [key, value] of Object.entries(cn).filter(([key]) => key.startsWith('forecast.'))) {
    const converted = convert(value).replace(/程式碼/g, '代碼').replace(/視窗/g, '區間')
    expect(tw[key as keyof typeof tw], key).toBe(converted)
  }
})

it('pins the approved disclaimer and describes both sides of the hit predicate', () => {
  expect(cn['forecast.disclaimer']).toBe('研究验证用途，不构成投资建议')
  expect(en['forecast.disclaimer']).toBe('For research verification only. Not investment advice.')
  expect(en['forecast.metric.hit.help']).toContain('|upside| ≥ 5%')
  expect(en['forecast.metric.hit.help']).toContain('flat counts as a miss')
})

// The record page, its navigation, the OU field and the refusals it can show are copy a reader meets
// without any admin knowledge, so each must exist in every language rather than echo a key.
it('carries the record page, access field and refusal copy in all three languages', () => {
  const keys = [
    'nav.forecast_record',
    'forecast.record.title', 'forecast.record.population_all', 'forecast.record.population_own',
    'forecast.record.population_own_unnamed', 'forecast.record.empty', 'forecast.record.empty_dated',
    'forecast.record.empty_expected', 'forecast.record.unavailable', 'forecast.record.busy',
    'forecast.scope.all', 'forecast.scope.own', 'forecast.scope.none', 'forecast.scope.inherit',
    'forecast.scope.help', 'forecast.scope.external_note',
    'forecast.cell.min_n_met', 'forecast.cell.small_nocount', 'forecast.cell.pending_plain',
    'err.forecast_unavailable', 'err.forecast_custom_not_allowed', 'err.forecast_busy',
    'err.forecast_rate_limited', 'err.forecast_bad_scope',
  ]
  for (const [name, bundle] of Object.entries({ 'zh-CN': cn, 'zh-TW': tw, 'en-US': en })) {
    for (const key of keys) {
      const value = (bundle as Record<string, string>)[key]
      expect(typeof value === 'string' && value.length > 0, `${name} ${key}`).toBe(true)
    }
  }
  // Placeholders must survive in every language, or a name or date silently drops out.
  for (const bundle of [cn, tw, en] as Record<string, string>[]) {
    expect(bundle['forecast.record.population_own']).toContain('{{ou}}')
    expect(bundle['forecast.record.empty_dated']).toContain('{{date}}')
    expect(bundle['forecast.record.empty_expected']).toContain('{{date}}')
  }
  // The approved wording carries its n >= 30 gate verbatim in every language.
  for (const bundle of [cn, tw, en] as Record<string, string>[]) expect(bundle['forecast.cell.min_n_met']).toBe('n ≥ 30')
})
