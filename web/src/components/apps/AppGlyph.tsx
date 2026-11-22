import { AppstoreOutlined } from '@ant-design/icons'
import { DISPLAY_ICONS } from '../../lib/appDisplayV2'

const EMOJI = /\p{Extended_Pictographic}/u

/**
 * The one glyph for an app's `icon` string. A known display key renders its antd icon; a short legacy
 * emoji renders as text; anything else (an unknown word, empty) is the generic app glyph, so a raw key
 * is never printed. `size` is the font size in px; a future image URL prop can slot in here.
 */
export default function AppGlyph({ icon, size }: { icon?: string; size?: number }) {
  const style = size ? { fontSize: size } : undefined
  const Known = icon ? (DISPLAY_ICONS as Record<string, typeof AppstoreOutlined>)[icon] : undefined
  if (Known) return <Known aria-hidden style={style} />
  if (icon && [...icon].length <= 4 && EMOJI.test(icon)) return <span aria-hidden style={{ lineHeight: 1, ...style }}>{icon}</span>
  return <AppstoreOutlined aria-hidden style={style} />
}
