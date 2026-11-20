// ianaZones lists the browser's full IANA zone list (Intl.supportedValuesOf, guarded for older
// engines, where it returns an empty list rather than throwing).
export function ianaZones(): string[] {
  try {
    return (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? []
  } catch {
    return []
  }
}

// tzOptions is a Select's zone choices: a default entry whose value is '' and whose label the
// caller names ("follow system", "the reader's zone"), then every IANA zone.
export function tzOptions(defaultLabel: string) {
  return [{ value: '', label: defaultLabel }, ...ianaZones().map((z) => ({ value: z, label: z }))]
}
