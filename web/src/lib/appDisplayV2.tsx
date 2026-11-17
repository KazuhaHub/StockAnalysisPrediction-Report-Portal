import { AppstoreOutlined, FileTextOutlined, DashboardOutlined, CalendarOutlined, BarChartOutlined, CloudOutlined, ToolOutlined, ExperimentOutlined } from '@ant-design/icons'
import { DESCRIPTOR_DISPLAY_ICONS, DESCRIPTOR_LOCALES, DESCRIPTOR_LIMITS, DESCRIPTOR_FIELDS, type AppDisplay } from './descriptor-contract.generated'

function record(value: unknown, fields: readonly string[]): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(key => fields.includes(key))
}
function text(value: unknown, max: number): value is string {
  return typeof value === 'string' && value.length > 0 && [...value].length <= max && value.trim() === value && !/[\p{Cc}\p{Cf}\uD800-\uDFFF\uFFFD]/u.test(value)
}
export function parseAppDisplay(value: unknown): AppDisplay | null {
  if (!record(value, DESCRIPTOR_FIELDS.AppDisplay) || typeof value.icon !== 'string' || !(DESCRIPTOR_DISPLAY_ICONS as readonly string[]).includes(value.icon) || !Array.isArray(value.texts) || value.texts.length !== DESCRIPTOR_LOCALES.length) return null
  const texts: AppDisplay['texts'] = []
  const seen = new Set<string>()
  for (const item of value.texts) {
    if (!record(item, DESCRIPTOR_FIELDS.AppDisplayText) || typeof item.locale !== 'string' || !(DESCRIPTOR_LOCALES as readonly string[]).includes(item.locale) || seen.has(item.locale) || !text(item.name, DESCRIPTOR_LIMITS.MaxAppNameCharacters) || !text(item.description, DESCRIPTOR_LIMITS.MaxAppDescriptionCharacters)) return null
    seen.add(item.locale)
    texts.push({ locale: item.locale as AppDisplay['texts'][number]['locale'], name: item.name, description: item.description })
  }
  return { icon: value.icon as AppDisplay['icon'], texts }
}
export function appDisplayText(id: string, display: AppDisplay | null | undefined, locale: string) {
  const copy = display?.texts.find(text => text.locale === locale)
  return copy ? { name: copy.name, description: copy.description } : { name: id, description: '' }
}
const icons = { appstore: AppstoreOutlined, 'file-text': FileTextOutlined, dashboard: DashboardOutlined, calendar: CalendarOutlined, 'bar-chart': BarChartOutlined, cloud: CloudOutlined, tool: ToolOutlined, experiment: ExperimentOutlined }
export function AppDisplayIcon({ display }: { display?: AppDisplay | null }) {
  const Icon = display ? icons[display.icon] : AppstoreOutlined
  return <Icon aria-hidden />
}
