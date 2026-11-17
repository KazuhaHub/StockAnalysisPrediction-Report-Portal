// Mermaid 12.1 lays flowchart, state, class, ER and requirement diagrams out with ELK unless a layout
// is requested, and the ELK engine (elkjs, EPL-2.0) is aliased out of the build (vite.config.ts,
// doc 167 section 8). Those diagrams therefore ask for dagre.
//
// Not every diagram, though: once a layout is set, mermaid treats it as the user's choice and a mind
// map stops using its own cose-bilkent layout. A type listed here keeps its own layout, and its
// option is left unset. A type nobody has classified gets dagre, so an ELK default is never reached.
export const NATIVE_LAYOUT_TYPES: ReadonlySet<string> = new Set(['mindmap'])

export function layoutForDiagram(type: string | undefined): 'dagre' | undefined {
  return type !== undefined && NATIVE_LAYOUT_TYPES.has(type) ? undefined : 'dagre'
}
