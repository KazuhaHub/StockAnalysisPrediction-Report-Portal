// Which admin forms currently hold unsaved edits. usePendingSettings reports into this, and a
// reload that the connection coming back would trigger (useLoadFailure) consults it: refreshing a
// form from the server while it is dirty would overwrite what the admin typed.
const dirty = new Set<string>()

export function markDraft(id: string, isDirty: boolean) {
  if (isDirty) dirty.add(id)
  else dirty.delete(id)
}

export const hasPendingDrafts = () => dirty.size > 0
