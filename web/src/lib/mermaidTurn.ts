// mermaid.initialize configures one global instance, synchronously, while parse and render are asynchronous.
// Two diagrams on a page therefore cannot each initialize and then render on their own: the last initialize
// would be in force for both renders. A diagram's whole step (initialize, parse, render) runs as one turn,
// and turns run one at a time in the order they were asked for.
//
// A per-diagram directive in the source was considered and rejected: it rewrites the text that is parsed,
// cached and exported, and the layout would then depend on the author's own directives.
let last: Promise<unknown> = Promise.resolve()

export function inMermaidTurn<T>(step: () => Promise<T>): Promise<T> {
  const turn = last.then(step)
  // A failed turn must not break the chain for the ones after it.
  last = turn.catch(() => undefined)
  return turn
}
