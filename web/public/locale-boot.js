// Runs after the boot splash is parsed and before the app bundle, so the tab title, the splash's
// site name and <html lang> are already in the reader's language on the first paint. index.html
// ships the base language's name; without this an English or Traditional Chinese reader saw it
// until React mounted. The CSP has no 'unsafe-inline', which is why this is a same-origin file.
//
// It mirrors src/locales/index.ts (normalizeSaved, matchLang, detectLang) and the brand strings in
// src/locales/*.json; src/test/localeBoot.test.ts checks both against the app's own code. A site
// title the administrator configured is injected by the server in place of the default, so the
// default is the only title this script ever replaces.
;(function () {
  var DEFAULT = '研报门户'
  var BRAND = { 'zh-CN': '研报门户', 'zh-TW': '研報門戶', 'en-US': 'Research Portal' }
  var BASE = 'zh-CN'

  function supported(code) {
    return Object.prototype.hasOwnProperty.call(BRAND, code)
  }

  function normalizeSaved(v) {
    if (v === 'zh') return 'zh-CN'
    if (v === 'en') return 'en-US'
    return v && supported(v) ? v : BASE
  }

  function matchLang(raw) {
    var sub = String(raw).toLowerCase().split('-')
    var tag = sub.join('-')
    for (var code in BRAND) {
      if (supported(code) && code.toLowerCase() === tag) return code
    }
    if (sub[0] === 'zh') {
      if (sub.indexOf('hans') >= 0) return 'zh-CN'
      if (sub.indexOf('hant') >= 0) return 'zh-TW'
      return sub.indexOf('tw') >= 0 || sub.indexOf('hk') >= 0 || sub.indexOf('mo') >= 0 ? 'zh-TW' : 'zh-CN'
    }
    for (var c in BRAND) {
      if (supported(c) && c.toLowerCase().split('-')[0] === sub[0]) return c
    }
    return undefined
  }

  function detectLang() {
    var nav = window.navigator || {}
    var list = nav.languages != null ? nav.languages : [nav.language]
    for (var i = 0; i < list.length; i++) {
      var m = list[i] && matchLang(list[i])
      if (m) return m
    }
    return BASE
  }

  var saved = null
  try {
    saved = window.localStorage.getItem('rp_locale')
  } catch (e) {
    saved = null
  }
  var lang = saved ? normalizeSaved(saved) : detectLang()
  var brand = BRAND[lang]

  document.documentElement.lang = lang
  if (document.title === DEFAULT) document.title = brand
  var name = document.getElementById('boot-name')
  if (name && name.textContent === DEFAULT) name.textContent = brand
})()
