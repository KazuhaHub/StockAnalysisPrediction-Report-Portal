// Hand-drawn SVG icons (no emoji): sun/moon/auto for theme switching, plus the brand (candlestick) mark in the top-left corner.
// All use currentColor so they follow the text color/theme; size is 1em so they track fontSize.

type IconProps = { style?: React.CSSProperties; className?: string }

const base = (extra?: React.CSSProperties): React.SVGProps<SVGSVGElement> => ({
  viewBox: '0 0 24 24',
  width: '1em',
  height: '1em',
  focusable: false,
  'aria-hidden': true,
  style: { display: 'inline-block', verticalAlign: '-0.15em', ...extra },
})

// Light mode: sun
export function SunIcon({ style, className }: IconProps) {
  return (
    <svg {...base(style)} className={className} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round">
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </svg>
  )
}

// Dark mode: moon
export function MoonIcon({ style, className }: IconProps) {
  return (
    <svg {...base(style)} className={className} fill="currentColor">
      <path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z" />
    </svg>
  )
}

// Follow system: a display. The half-filled circle this used to be is the "contrast" idea, which
// reads as a brightness setting rather than as "whatever this device is set to" — and at 14px the
// half-fill was the least legible of the three glyphs. A display is what the operating systems and
// browsers use for the same choice.
export function AutoIcon({ style, className }: IconProps) {
  return (
    <svg {...base(style)} className={className} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round">
      <rect x="3" y="4" width="18" height="12.5" rx="2" />
      <path d="M9 20.5h6M12 16.5v4" />
    </svg>
  )
}

// Brand: candlestick / line chart (replaces 📈). Uses currentColor by default; pass style.color to apply a theme color.
export function BrandIcon({ style, className }: IconProps) {
  return (
    <svg
      {...base(style)}
      className={className}
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M4 4v16h16" />
      <path d="M7 14.5l3.5-4 3 3L20 7" />
      <circle cx="20" cy="7" r="1.4" fill="currentColor" stroke="none" />
    </svg>
  )
}
