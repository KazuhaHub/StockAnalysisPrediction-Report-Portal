import { expect, test } from 'vitest'
import { parseAppDisplay, appDisplayText } from './appDisplayV2'
const display={icon:'file-text',texts:[{locale:'en-US',name:'Notes',description:'Personal notes'},{locale:'zh-CN',name:'笔记',description:'个人笔记'},{locale:'zh-TW',name:'筆記',description:'個人筆記'}]}
test('display follows the panel locale and missing experimental metadata has a plain ID fallback',()=>{
 const parsed=parseAppDisplay(display)
 expect(parsed).toEqual(display)
 expect(appDisplayText('notes-sample',parsed,'zh-TW').name).toBe('筆記')
 expect(appDisplayText('notes-sample',undefined,'zh-CN')).toEqual({name:'notes-sample',description:''})
})
test('display refuses incomplete copy, untrusted icon URLs, control characters and oversized names',()=>{
 for(const bad of [
  {...display,icon:'https://evil.invalid/icon'},
  {...display,texts:display.texts.slice(0,2)},
  {...display,texts:[display.texts[0],display.texts[0],display.texts[2]]},
  {...display,texts:[{...display.texts[0],name:'Notes\u202e'},...display.texts.slice(1)]},
  {...display,texts:[{...display.texts[0],name:'笔'.repeat(81)},...display.texts.slice(1)]},
 ]) expect(parseAppDisplay(bad)).toBeNull()
})
