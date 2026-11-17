/// <reference types="vitest/config" />
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'
import { forbidElkjs } from './scripts/forbid-elkjs.mjs'

// In dev, proxy API / downloads / health check to the Go backend (:8790). The
// production build is written to ../internal/web/dist for //go:embed.
export default defineConfig({
  // forbidElkjs fails the build if the alias below ever stops holding: no module under node_modules/elkjs.
  plugins: [react(), forbidElkjs()],
  resolve: {
    alias: [
      // mermaid 12 imports the ELK layout engine (elkjs, EPL-2.0) for an optional layout this
      // portal does not offer. Every import path of it resolves to an empty module that throws,
      // so the engine is not bundled and an ELK diagram fails closed (doc 167 section 8). The
      // build test scripts/check-no-elk.mjs proves it, and the licence gate's exclusion for
      // elkjs depends on that test.
      { find: /^elkjs(\/.*)?$/, replacement: fileURLToPath(new URL('./src/lib/elkDisabled.ts', import.meta.url)) },
    ],
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8790',
      '/app-assets': 'http://localhost:8790', // installed iframe apps' static files (ADR 0003)
      '/site-assets': 'http://localhost:8790',
      '/report': 'http://localhost:8790',
      '/healthz': 'http://localhost:8790',
      '/manifest.webmanifest': 'http://localhost:8790',
      '/pwa-icon': 'http://localhost:8790',
    },
  },
  build: {
    // Keep emptyOutDir false so the committed .gitkeep survives — that lets
    // `go build` compile the embed on a fresh clone before the SPA is built.
    outDir: '../internal/web/dist',
    emptyOutDir: false,
    chunkSizeWarningLimit: 1600,
    rollupOptions: {
      output: {
        // Keep React in a stable vendor chunk. Ant Design is deliberately left to
        // Rollup: forcing every antd/rc module into one manual chunk made code used
        // only by lazy admin routes part of the initial module-preload graph.
        // Route-only dependencies (markdown, dnd-kit, admin-only antd components)
        // can now remain behind their route boundary.
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('/react/') || id.includes('/react-dom/') || id.includes('/react-router') || id.includes('/scheduler/')) return 'react'
        },
      },
    },
  },
  test: {
    // jsdom for component tests; explicit imports (no globals) keep tsc happy.
    environment: 'jsdom',
    globals: false,
    setupFiles: ['src/test/setup.ts'],
    // Node 24+ exposes an experimental process-wide localStorage. jsdom supplies the browser
    // storage used by tests; disabling Node's copy in workers avoids one warning per test file.
    execArgv: process.allowedNodeEnvironmentFlags.has('--no-experimental-webstorage')
      ? ['--no-experimental-webstorage']
      : [],
    include: ['src/**/*.test.{ts,tsx}'],
    // Cap the worker pool. Left uncapped, vitest runs as many test files at once as there are
    // cores, and the suite then competes with itself: assertions that wait for a React effect or a
    // resolved promise lose the race and report the pre-update value as if it were final. The
    // failures roamed — Markdown, site settings, types, version-check — which is the signature of
    // CPU contention rather than of any one test being wrong.
    maxWorkers: 4,
    // The other half of the same problem. Raising testing-library's async budget stopped assertions
    // reporting a pre-update value, but each TEST still had vitest's 5s ceiling — and under a
    // contended pool a whole test can legitimately take longer than that, which reports as
    // "Test timed out" rather than as a wrong answer. Costs a passing run nothing: a test returns
    // when it is done, and only a genuinely stuck one now takes longer to say so.
    testTimeout: 20000,
  },
})
