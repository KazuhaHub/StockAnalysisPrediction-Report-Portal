import { describe, expect, it } from 'vitest'
import { layoutForDiagram, NATIVE_LAYOUT_TYPES } from './mermaidLayout'

// Mermaid 12.1 lays most diagrams out with ELK unless asked otherwise, and the ELK engine is not
// in the build (doc 167 section 8). Forcing dagre on EVERY diagram also changes a mind map: mermaid
// then sees a user-chosen layout and stops using its own cose-bilkent one. So dagre is requested
// only for the diagram types that would otherwise reach ELK.
describe('layoutForDiagram', () => {
  it('asks for dagre for the diagram types that default to ELK', () => {
    for (const type of ['flowchart-v2', 'flowchart', 'stateDiagram', 'classDiagram', 'er', 'requirement']) {
      expect(layoutForDiagram(type), type).toBe('dagre')
    }
  })

  it('leaves the layout unset for a mind map, which brings its own', () => {
    expect(layoutForDiagram('mindmap')).toBeUndefined()
    expect(NATIVE_LAYOUT_TYPES.has('mindmap')).toBe(true)
  })

  it('asks for dagre when the type is not known, so an ELK default can never be reached', () => {
    expect(layoutForDiagram(undefined)).toBe('dagre')
    expect(layoutForDiagram('some-new-diagram')).toBe('dagre')
  })
})
