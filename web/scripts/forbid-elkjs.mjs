// Build-time guard: no module of the ELK layout engine (elkjs, EPL-2.0) may be part of the bundle.
//
// vite.config.ts aliases every elkjs import to src/lib/elkDisabled.ts. This plugin is the check that the
// alias held: a Rollup plugin that fails the build if any resolved module id lies under node_modules/elkjs/.
// It looks at module ids, not at text, so it also catches elkjs's thin wrappers (lib/elk-api.js, lib/main.js)
// that carry none of the engine's class names. scripts/check-no-elk.mjs scans the output text as a second layer.
const ELKJS = /[\\/]node_modules[\\/]elkjs[\\/]/

export function elkjsModules(ids) {
  return ids.filter((id) => ELKJS.test(id))
}

function fail(where, ids) {
  throw new Error(
    `elkjs (EPL-2.0) must not be bundled, but ${where} contains: ${ids.join(', ')}. ` +
      'The elkjs alias in vite.config.ts is not taking effect; see docs/app-platform/173-licence-policy-plan.md.',
  )
}

export function forbidElkjs() {
  return {
    name: 'forbid-elkjs',
    // As soon as a module is parsed, so a failure names the import that pulled it in.
    moduleParsed(info) {
      if (ELKJS.test(info.id)) fail('the module graph', [info.id])
    },
    generateBundle(_options, bundle) {
      for (const [name, item] of Object.entries(bundle)) {
        if (item.type !== 'chunk') continue
        const hits = elkjsModules(Object.keys(item.modules ?? {}))
        if (hits.length > 0) fail(`chunk ${item.fileName ?? name}`, hits)
      }
    },
  }
}
