import { useState } from 'react'

/** Keep the acknowledged configuration separate from the editable draft. */
export default function useSettingsDraft<T extends object>(initial: T) {
  const [value, setValue] = useState(initial)
  const [saved, setSaved] = useState<T | null>(null)
  const accept = (next: T) => { setValue(next); setSaved(next) }
  return {
    value,
    saved,
    dirty: saved !== null && JSON.stringify(value) !== JSON.stringify(saved),
    accept,
    acknowledge: setSaved,
    patch: (patch: Partial<T>) => setValue((current) => ({ ...current, ...patch })),
    discard: () => { if (saved) setValue(saved) },
  }
}
