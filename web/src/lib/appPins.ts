import type { AppLayoutEntry } from '../api/types'
import { appDisplayText, parseAppDisplay } from './appDisplayV2'
import { validInstallationId } from './appLaunchV2'
import { PLATFORM_PIN_PREFIX } from './shortcuts'

/**
 * The platform installations an entry button can be pinned to, from the administrator's layout: value
 * `platform:<installation id>`, label the display name in the language given (else the layout's own name, else
 * the application id). Built-in features, retired v1 apps and removed installations (the layout does not list
 * them) are not offered.
 */
export function platformPinOptions(entries: readonly AppLayoutEntry[], lang: string): { value: string; label: string }[] {
  const options: { value: string; label: string }[] = []
  for (const e of entries) {
    if (e.kind !== 'platform' || !e.installation_id || !validInstallationId(e.installation_id)) continue
    const display = e.display == null ? null : parseAppDisplay(e.display)
    const shown = display ? appDisplayText(e.app_id ?? '', display, lang) : null
    const label = shown && shown.name !== (e.app_id ?? '') ? shown.name : e.name || e.app_id || e.installation_id
    options.push({ value: PLATFORM_PIN_PREFIX + e.installation_id, label })
  }
  return options
}
