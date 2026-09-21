import { describe, it, expect } from 'vitest'
import { selectNotesLanguage } from './releaseNotes'

// Release notes are bilingual from now on, and the panel picks the reader's language out of them.
//
// The convention is a marker per section — `<zh-CN>` … `</zh-CN>` — because the same text is also the
// GitHub Release body, where an unknown tag renders as nothing and leaves the prose readable. The
// portal does the opposite: it keeps exactly one section, so a reader never sees two languages of the
// same note stacked.

const BOTH = `<zh-CN>

## 本次更新

- 中文正文。

</zh-CN>

<en-US>

## What changed

- English body.

</en-US>`

describe('selectNotesLanguage', () => {
  it('takes the section for the reader’s language', () => {
    expect(selectNotesLanguage(BOTH, 'en-US')).toBe('## What changed\n\n- English body.')
    expect(selectNotesLanguage(BOTH, 'zh-CN')).toBe('## 本次更新\n\n- 中文正文。')
  })

  // Traditional Chinese is the same language; a note that has no zh-TW section is still readable to a
  // zh-TW reader, and showing them the English instead would be a worse answer than the one sitting
  // right there.
  it('gives a zh-TW reader the Simplified section rather than nothing', () => {
    expect(selectNotesLanguage(BOTH, 'zh-TW')).toBe('## 本次更新\n\n- 中文正文。')
  })

  it('matches a regional variant to its base language', () => {
    expect(selectNotesLanguage(BOTH, 'en-GB')).toBe('## What changed\n\n- English body.')
    expect(selectNotesLanguage(BOTH, 'zh-Hans')).toBe('## 本次更新\n\n- 中文正文。')
  })

  it('falls back to the first section when the language is not there at all', () => {
    expect(selectNotesLanguage(BOTH, 'ja-JP')).toBe('## 本次更新\n\n- 中文正文。')
  })

  // Every note published before this convention is English-only and untagged, and those dialogs have
  // to keep working exactly as they did.
  it('returns an untagged note unchanged', () => {
    const plain = '## What changed\n\n- English only.'
    expect(selectNotesLanguage(plain, 'zh-TW')).toBe(plain)
  })

  it('returns a single-section note to every reader', () => {
    const only = '<en-US>\n\n## Only English\n</en-US>'
    expect(selectNotesLanguage(only, 'zh-CN')).toBe('## Only English')
  })

  it('is not confused by case or spacing in the marker', () => {
    const odd = '<ZH-cn>\n\n中文\n\n</zh-CN>'
    expect(selectNotesLanguage(odd, 'zh-CN')).toBe('中文')
  })

  // A note with a missing closing tag is a note somebody wrote by hand in a hurry. Holding the rest
  // of it as that section is the forgiving reading; refusing to render anything is not.
  it('runs an unclosed section to the end', () => {
    const open = '<zh-CN>\n\n中文\n\n<en-US>\n\nEnglish\n'
    expect(selectNotesLanguage(open, 'zh-CN')).toBe('中文')
    expect(selectNotesLanguage(open, 'en-US')).toBe('English')
  })

  it('ignores anything outside the sections when there are sections', () => {
    const pad = 'preamble\n\n<zh-CN>\n\n中文\n\n</zh-CN>\n\ntrailing'
    expect(selectNotesLanguage(pad, 'zh-CN')).toBe('中文')
  })

  // GitHub renders these notes too, where the marker is an unknown tag that the sanitizer drops and
  // the prose stays. The portal's own reader must therefore not treat a `<...>` tag that is not a
  // locale marker as a section — a note containing HTML would otherwise be chopped up.
  it('leaves other angle-bracket content alone', () => {
    const html = 'Use <code>docker pull</code> and <b>bold</b> here.'
    expect(selectNotesLanguage(html, 'zh-CN')).toBe(html)
  })
})
