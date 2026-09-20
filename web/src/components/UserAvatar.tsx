import { Avatar } from 'antd'

// A name-derived avatar, the way a webmail client draws one: no image to store or serve, and the
// same account reads the same in every list it appears in.
//
// The colour comes from `seed` — the username, which does not change — while the letters come from
// `name`, the display name, which can. Renaming yourself therefore changes the letters and not the
// colour, and a row that colours by username keeps its colour when a display name is added.
const AVATAR_COLORS = ['#1677ff', '#52c41a', '#faad14', '#eb2f96', '#722ed1', '#13c2c2', '#fa541c']

export function avatarColor(s: string) {
  let h = 0
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0
  return AVATAR_COLORS[h % AVATAR_COLORS.length]
}

export function initials(s: string) {
  const t = s.trim()
  if (!t) return '?'
  // First glyph works for CJK; for latin words take up to two initials.
  const parts = t.split(/\s+/)
  if (parts.length > 1) return (parts[0][0] + parts[1][0]).toUpperCase()
  return t.slice(0, /[一-龥]/.test(t) ? 1 : 2).toUpperCase()
}

export default function UserAvatar({
  name,
  seed,
  size = 'default',
  className,
}: {
  /** What the letters are drawn from: the display name where there is one, else the username. */
  name: string
  /** What the colour is drawn from. Defaults to `name`. */
  seed?: string
  /** Ant Design's presets, so the letters scale with the circle — a bare number sets the box only. */
  size?: number | 'small' | 'default' | 'large'
  className?: string
}) {
  return (
    <Avatar size={size} className={className} style={{ backgroundColor: avatarColor(seed || name), flexShrink: 0 }}>
      {initials(name)}
    </Avatar>
  )
}
