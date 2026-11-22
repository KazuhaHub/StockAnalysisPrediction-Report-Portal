/** Joins short names for display the way the language does: 、 in Chinese, a comma and space elsewhere. */
export function joinList(items: readonly string[], lang: string): string {
  return items.join(lang.toLowerCase().startsWith('zh') ? '、' : ', ')
}
