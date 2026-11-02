import { theme, type ThemeConfig } from 'antd'
import { RADIUS } from './radius'

const PRIMARY = '#0958d9'

export function portalThemeToken(dark: boolean) {
  return {
    colorPrimary: PRIMARY,
    colorLink: dark ? '#69b1ff' : PRIMARY,
    colorLinkHover: dark ? '#91caff' : '#003eb3',
    colorLinkActive: dark ? '#bae0ff' : '#002766',
    colorSuccess: dark ? '#73d13d' : '#237804',
    colorSuccessBg: dark ? '#162312' : '#f6ffed',
    colorSuccessBorder: dark ? '#274916' : '#b7eb8f',
    colorSuccessText: dark ? '#73d13d' : '#237804',
    colorTextSecondary: dark ? '#bfbfbf' : '#595959',
    colorTextDescription: dark ? '#bfbfbf' : '#595959',
    colorTextTertiary: dark ? '#8c8c8c' : '#737373',
    colorTextPlaceholder: dark ? '#999999' : '#595959',
    borderRadius: RADIUS.base,
    borderRadiusXS: RADIUS.xs,
    borderRadiusSM: RADIUS.sm,
    borderRadiusLG: RADIUS.lg,
  }
}

export function portalTheme(dark: boolean): ThemeConfig {
  return {
    algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: portalThemeToken(dark),
    // One radius scale for the portal. Surfaces use LG; controls keep the base
    // radius at every size so a large control does not look unrelated.
    components: {
      Divider: { orientationMargin: 0 },
      Button: { borderRadiusLG: RADIUS.base },
      Input: { borderRadiusLG: RADIUS.base },
      InputNumber: { borderRadiusLG: RADIUS.base },
      Select: { borderRadiusLG: RADIUS.base },
      DatePicker: { borderRadiusLG: RADIUS.base },
    },
    // Each palette needs its own key so switching themes regenerates neutral
    // CSS variables instead of leaving controls on the previous palette.
    cssVar: { key: dark ? 'rp-dark' : 'rp-light' },
  }
}
