export function elkjsModules(ids: string[]): string[]
export function forbidElkjs(): {
  name: string
  moduleParsed(info: { id: string }): void
  generateBundle(options: unknown, bundle: Record<string, { type: string; fileName?: string; modules?: Record<string, unknown> }>): void
}
