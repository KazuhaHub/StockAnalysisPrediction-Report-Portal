import { useState } from 'react'
import { AppstoreOutlined, FileTextOutlined, DashboardOutlined, CalendarOutlined, BarChartOutlined, CloudOutlined, ToolOutlined, ExperimentOutlined } from '@ant-design/icons'
import { DESCRIPTOR_DISPLAY_ICONS, DESCRIPTOR_LOCALES, DESCRIPTOR_LIMITS, DESCRIPTOR_FIELDS, type AppDisplay } from './descriptor-contract.generated'

function record(value: unknown, fields: readonly string[]): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(key => fields.includes(key))
}
function text(value: unknown, max: number): value is string {
  return typeof value === 'string' && value.length > 0 && [...value].length <= max && value.trim() === value && !/[\p{Cc}\p{Cf}\uD800-\uDFFF\uFFFD]/u.test(value)
}
/**
 * A display as the server sends it: the signed descriptor's display with a custom icon replaced by its hash.
 * The bytes are never in a response; the image is fetched from /api/app-platform/icons/<sha>.png.
 */
export type AppDisplayView = Omit<AppDisplay, 'icon_png'> & { icon_sha256?: string }
const VIEW_FIELDS = [...DESCRIPTOR_FIELDS.AppDisplay.filter(field => field !== 'icon_png'), 'icon_sha256']
const SHA256 = /^[0-9a-f]{64}$/
export function parseAppDisplay(value: unknown): AppDisplayView | null {
  if (!record(value, VIEW_FIELDS) || (value.icon_sha256 !== undefined && (typeof value.icon_sha256 !== 'string' || !SHA256.test(value.icon_sha256))) || typeof value.icon !== 'string' || !(DESCRIPTOR_DISPLAY_ICONS as readonly string[]).includes(value.icon) || !Array.isArray(value.texts) || value.texts.length !== DESCRIPTOR_LOCALES.length) return null
  const texts: AppDisplayView['texts'] = []
  const seen = new Set<string>()
  for (const item of value.texts) {
    if (!record(item, DESCRIPTOR_FIELDS.AppDisplayText) || typeof item.locale !== 'string' || !(DESCRIPTOR_LOCALES as readonly string[]).includes(item.locale) || seen.has(item.locale) || !text(item.name, DESCRIPTOR_LIMITS.MaxAppNameCharacters) || !text(item.description, DESCRIPTOR_LIMITS.MaxAppDescriptionCharacters)) return null
    seen.add(item.locale)
    texts.push({ locale: item.locale as AppDisplayView['texts'][number]['locale'], name: item.name, description: item.description })
  }
  return { icon: value.icon as AppDisplayView['icon'], ...(typeof value.icon_sha256 === 'string' ? { icon_sha256: value.icon_sha256 } : {}), texts }
}
export function appDisplayText(id: string, display: AppDisplayView | null | undefined, locale: string) {
  const copy = display?.texts.find(text => text.locale === locale)
  return copy ? { name: copy.name, description: copy.description } : { name: id, description: '' }
}
const icons = { appstore: AppstoreOutlined, 'file-text': FileTextOutlined, dashboard: DashboardOutlined, calendar: CalendarOutlined, 'bar-chart': BarChartOutlined, cloud: CloudOutlined, tool: ToolOutlined, experiment: ExperimentOutlined }
/** The signed custom icon when there is one and it loads; otherwise the built-in glyph the descriptor names. */
export function AppDisplayIcon({ display, iconSha256 }: { display?: AppDisplayView | null; iconSha256?: string }) {
  const Icon = display ? icons[display.icon] : AppstoreOutlined
  const sha = iconSha256 ?? display?.icon_sha256
  const [failed, setFailed] = useState<string | undefined>()
  if (sha && SHA256.test(sha) && failed !== sha) {
    return <img src={`/api/app-platform/icons/${sha}.png`} alt="" aria-hidden width={24} height={24} className="rp-app-icon-img" style={{ width: '1em', height: '1em', objectFit: 'contain', verticalAlign: '-0.125em' }} onError={() => setFailed(sha)} />
  }
  return <Icon aria-hidden />
}
/** The name to show for a publisher: its self-declared name, else its ID. Trust stays keyed on the ID. */
export const publisherLabel = (name: string | undefined, id: string) => name || id
/** Details views keep the ID visible: the name when there is one, with the ID that trust is keyed on beside it. */
export const publisherWithId = (name: string | undefined, id: string) => (name ? `${name} (${id})` : id)
