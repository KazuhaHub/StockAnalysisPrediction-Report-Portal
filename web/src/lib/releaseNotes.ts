// Bilingual release notes, and how the panel picks one language out of them.
//
// The note that seeds a GitHub Release is also what the portal's update dialog renders, so the same
// text has to read well in both places:
//
//   - on GitHub an unknown tag renders as nothing, and the two languages read as consecutive sections
//     — which is what a reader of the release page wants anyway;
//   - in the portal exactly ONE section is kept, chosen by the language the reader is using, because
//     stacking both would double the length of the dialog for no reader's benefit.
//
// The markers are the locale codes themselves (`<zh-CN>` … `</zh-CN>`), so the mapping is not a second
// vocabulary to maintain. Notes published before this convention carry no markers and are returned
// unchanged, which is what makes the change invisible to every release already out there.

/** A marker line: `<xx>` or `<xx-YY>`, and nothing else on the line. */
const OPEN = /^<([a-z]{2,3}(?:-[a-z0-9]{2,8})*)>$/i
const CLOSE = /^<\/([a-z]{2,3}(?:-[a-z0-9]{2,8})*)>$/i

type Section = { lang: string; body: string }

/** sections splits a note into its tagged sections, in the order they appear. */
function sections(md: string): Section[] {
  const lines = md.split('\n')
  const out: Section[] = []
  let lang = ''
  let buf: string[] = []
  const flush = () => {
    if (lang) out.push({ lang, body: buf.join('\n').trim() })
    buf = []
  }
  for (const line of lines) {
    const open = OPEN.exec(line.trim())
    const close = CLOSE.exec(line.trim())
    if (open) {
      flush() // a new section ends the previous one, closed or not
      lang = open[1].toLowerCase()
      continue
    }
    if (close) {
      // A closing tag for a section that was never opened is stray markup rather than content.
      if (lang === close[1].toLowerCase()) flush()
      lang = ''
      continue
    }
    if (lang) buf.push(line)
  }
  flush() // an unclosed last section runs to the end
  return out
}

/** base is the language part of a locale: `zh-TW` → `zh`, `en-GB` → `en`. */
function base(locale: string): string {
  return locale.split('-')[0].toLowerCase()
}

/**
 * selectNotesLanguage returns the note as the given reader should see it.
 *
 * The chain, in order: the exact locale; another locale of the same language (a zh-TW reader gets the
 * Simplified section — the same text in a script they read, rather than the English they may not);
 * then the first section, since a note in some other language is still the note. A note with no
 * markers is returned as it is, and so is one whose tags match nothing.
 */
export function selectNotesLanguage(md: string, lang: string): string {
  const found = sections(md)
  if (found.length === 0) return md
  const want = lang.trim().toLowerCase()
  const exact = found.find((s) => s.lang === want)
  if (exact) return exact.body
  const sameLanguage = found.find((s) => base(s.lang) === base(want))
  if (sameLanguage) return sameLanguage.body
  return found[0].body
}
