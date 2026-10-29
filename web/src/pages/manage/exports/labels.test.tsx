import { describe, expect, it, vi } from 'vitest'
import { renderHook } from '@testing-library/react'
import en from '../../../locales/en-US.json'
import zhCN from '../../../locales/zh-CN.json'
import zhTW from '../../../locales/zh-TW.json'
import { useExportLabels } from './labels'

// Echo the key when it has no entry, as i18next does for a missing key with a default value.
vi.mock('react-i18next', () => {
  const t = (k: string, d?: string) => (typeof d === 'string' ? d : k)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

describe('forecast labels', () => {
  const { result } = renderHook(() => useExportLabels())
  it('falls back to identifiers and names enum values through the locale keys', () => {
    expect(result.current.value('below_entry', 'yes')).toBe('yes')
    expect(result.current.value('action', 'x')).toBe('x')
    expect(result.current.source('forecasts')).toBe('forecasts')
  })

  const keys = [
    'export.source.forecasts',
    'export.field.report_date',
    'export.field.stance',
    'export.field.latest_per_symbol',
    'export.field.below_entry',
    'export.column.set_id',
    'export.column.report_id',
    'export.column.made_at',
    'export.column.entry_price',
    'export.column.terminal_value',
    'export.column.latest_price',
    'export.column.latest_at',
    'export.column.vs_entry_pct',
    'export.value.yes',
    'export.value.no',
    'export.preset.belowEntry',
    'export.preset.belowEntryHint',
  ]
  for (const [name, bundle] of [['en-US', en], ['zh-CN', zhCN], ['zh-TW', zhTW]] as const) {
    it(`${name} defines every forecast key`, () => {
      const dict = bundle as Record<string, string>
      for (const k of keys) expect(dict[k], k).toBeTruthy()
    })
  }
  it('stance values are labelled by the existing forecast keys', () => {
    const dict = en as Record<string, string>
    expect(dict['forecast.stance.long']).toBeTruthy()
    expect(dict['forecast.stance.no_position']).toBeTruthy()
  })
})
