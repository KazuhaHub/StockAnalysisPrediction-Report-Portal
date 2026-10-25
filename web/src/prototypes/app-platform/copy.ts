import { createInstance } from 'i18next'
import en from './locales/en-US.json'
import cn from './locales/zh-CN.json'
import tw from './locales/zh-TW.json'

export const prototypeResources = {
  'en-US': { translation: en },
  'zh-CN': { translation: cn },
  'zh-TW': { translation: tw },
}
export function createPrototypeLanguage(language = 'zh-CN') {
  const instance = createInstance()
  void instance.init({
    resources: prototypeResources,
    lng: language,
    fallbackLng: 'en-US',
    initAsync: false,
    interpolation: { escapeValue: false },
  })
  return instance
}
