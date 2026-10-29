import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

type Lookup = (key: string, fallback?: string) => string

export interface ExportLabels {
  source: (key: string) => string
  field: (key: string) => string
  column: (key: string) => string
  op: (key: string) => string
  status: (key: string) => string
  format: (key: string) => string
  /** An enum value as the admin should read it: audit actions and target types are already named. */
  value: (field: string, value: string) => string
}

// Field, column and source keys are Go-defined identifiers, so the client names them. A key the
// backend adds before this bundle learns it falls back to the identifier rather than to nothing.
export function useExportLabels(): ExportLabels {
  const { t } = useTranslation()
  return useMemo(() => {
    const tl = t as unknown as Lookup
    return {
      source: (k) => tl(`export.source.${k}`, k),
      // A name shared by a filter field and a column (date, symbol, action...) is defined once, under
      // field; each lookup falls through to the other namespace before giving up on the identifier.
      field: (k) => tl(`export.field.${k}`, tl(`export.column.${k}`, k)),
      column: (k) => tl(`export.column.${k}`, tl(`export.field.${k}`, k)),
      op: (k) => tl(`export.op.${k}`, k),
      status: (k) => tl(`export.status.${k}`, k),
      format: (k) => tl(`export.format.${k}`, k),
      value: (field, v) => (field === 'action' ? tl(`audit.a.${v}`, v) : field === 'target_type' ? tl(`audit.t.${v}`, v) : v),
    }
  }, [t])
}
