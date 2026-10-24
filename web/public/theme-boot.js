// Runs before the app bundle, so the boot splash and native scrollbars already match the reader's
// theme on the first paint. The CSP has no 'unsafe-inline', which is why this is a same-origin file
// rather than an inline script. It mirrors prefs.tsx: a stored 'light' or 'dark' wins, and 'auto' or
// nothing stored follows the system. prefs.tsx keeps these attributes current once the app mounts.
;(function () {
  var stored = null
  try {
    stored = window.localStorage.getItem('rp_theme')
  } catch (e) {
    stored = null
  }
  var systemDark = !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches)
  var dark = stored === 'dark' || (stored !== 'light' && systemDark)
  var el = document.documentElement
  el.setAttribute('data-theme', dark ? 'dark' : 'light')
  el.style.colorScheme = dark ? 'dark' : 'light'
})()
