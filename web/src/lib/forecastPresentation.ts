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

// The coverage bar's segments. In the default theme antd's info colour is the primary colour, so a
// bar that drew "counted" in the primary and "repeat run" in the info colour drew them identically;
// the duplicate segment takes a lighter step of the same blue instead (a lighter one in dark mode,
// where the generated light steps are near-black), and the ledger-start segment is hatched so it is
// told apart by pattern as well as by shade.
export function coveragePalette(token: GlobalToken) {
  const dark = token.colorBgBase !== '#fff' && token.colorBgBase !== '#ffffff'
  return {
    member: token.colorPrimary,
    duplicate: dark ? token.blue7 : token.blue3,
    missing: token.colorWarning,
    other: token.colorTextQuaternary,
    preLive: `repeating-linear-gradient(135deg, ${token.colorTextTertiary} 0 3px, transparent 3px 6px)`,
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
