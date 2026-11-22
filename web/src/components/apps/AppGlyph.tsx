import { AppstoreOutlined } from '@ant-design/icons'
import { DISPLAY_ICONS } from '../../lib/appDisplayV2'

const PICTOGRAPH = /\p{Extended_Pictographic}|\p{Regional_Indicator}|\u20E3/u

// Exactly one grapheme cluster (a ZWJ family, a flag and a keycap each count as one) that is emoji-like.
function isSingleEmoji(value: string): boolean {
  if (!PICTOGRAPH.test(value)) return false
  const Segmenter = (Intl as { Segmenter?: new (l: string, o: { granularity: 'grapheme' }) => { segment(s: string): Iterable<unknown> } }).Segmenter
  if (!Segmenter) return [...value].length <= 4
  let count = 0
  for (const _ of new Segmenter('und', { granularity: 'grapheme' }).segment(value)) {
    void _
    if (++count > 1) return false
  }
  return count === 1
}

/**
 * The one glyph for an app's `icon` string. A known display key renders its antd icon; a single legacy
 * emoji renders as text; anything else (an unknown word, empty) is the generic app glyph, so a raw key
 * is never printed. `size` is the font size in px; a future image URL prop can slot in here.
 */
export default function AppGlyph({ icon, size }: { icon?: string; size?: number }) {
  const style = size ? { fontSize: size } : undefined
  const Known = icon && Object.prototype.hasOwnProperty.call(DISPLAY_ICONS, icon) ? DISPLAY_ICONS[icon as keyof typeof DISPLAY_ICONS] : undefined
  if (Known) return <Known aria-hidden style={style} />
  if (icon && isSingleEmoji(icon)) return <span aria-hidden style={{ lineHeight: 1, ...style }}>{icon}</span>
  return <AppstoreOutlined aria-hidden style={style} />
}
