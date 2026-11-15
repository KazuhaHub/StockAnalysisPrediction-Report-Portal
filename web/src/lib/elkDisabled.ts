// Stand-in for elkjs (EPL-2.0). vite.config.ts aliases every elkjs import here, so the ELK layout
// engine is not part of the build and mermaid's ELK layout is unsupported (doc 167 section 8).
// Mermaid constructs the engine only when a diagram asks for the ELK layout, and that diagram then
// fails with this message instead of being laid out.
export default class ELK {
  constructor() {
    throw new Error('The ELK layout is not supported in this build; use the default layout instead.')
  }
}
