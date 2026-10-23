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
