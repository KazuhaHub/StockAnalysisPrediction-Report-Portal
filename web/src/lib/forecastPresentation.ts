import type { GlobalToken } from 'antd'

// Palette steps are checked on the actual algorithm's tint, including custom seeds.
export function forecastPalette(token: GlobalToken) {
  const dark = token.colorBgBase !== '#fff' && token.colorBgBase !== '#ffffff'
  return {
    positive: { foreground: token[dark ? 'red8' : 'red7'], background: token.colorErrorBg },
    negative: { foreground: token[dark ? 'green6' : 'green8'], background: token.colorSuccessBg },
    neutral: { foreground: token.colorText, background: token.colorFillQuaternary },
  }
}

export function formatSigned(value: number | undefined, digits = 1): string {
  if (value === undefined || !Number.isFinite(value)) return '—'
  const text = value.toFixed(digits)
  return Number(text) > 0 ? `+${text}` : Number(text) === 0 ? (0).toFixed(digits) : text
}

export function formatProbability(value: number | undefined): string {
  return value === undefined || !Number.isFinite(value) ? '—' : value < 0.001 ? '<0.001' : value.toFixed(3)
}
