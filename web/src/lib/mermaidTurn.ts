// mermaid.initialize configures one global instance, synchronously, while parse and render are asynchronous.
// Two diagrams on a page therefore cannot each initialize and then render on their own: the last initialize
// would be in force for both renders. A diagram's whole step (initialize, parse, render) runs as one turn,
// and turns run one at a time in the order they were asked for.
//
// A per-diagram directive in the source was considered and rejected: it rewrites the text that is parsed,
// cached and exported, and the layout would then depend on the author's own directives.
//
// One turn that never settles would hold up every diagram after it, so a turn is given a time limit. When it
// runs out the turn is rejected (its block falls back to the source as a code block) and the next turn
// starts. The abandoned step cannot be cancelled, so it is told instead: `owned()` turns false when its turn
// times out, and the step must check it before touching the global configuration or the DOM again, or a late
// render would run beside the next turn's initialize and write a diagram drawn under another's layout.
export const MERMAID_TURN_TIMEOUT_MS = 15_000

let last: Promise<unknown> = Promise.resolve()

export type MermaidTurnStep<T> = (owned: () => boolean) => Promise<T>

function withTimeout<T>(step: MermaidTurnStep<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    let timedOut = false
    // The limit runs from the start of the turn, not from when it was asked for.
    const timer = setTimeout(() => {
      timedOut = true
      reject(new Error('mermaid turn timed out'))
    }, MERMAID_TURN_TIMEOUT_MS)
    step(() => !timedOut).then(
      (value) => {
        clearTimeout(timer)
        resolve(value)
      },
      (error: unknown) => {
        clearTimeout(timer)
        reject(error)
      },
    )
  })
}

export function inMermaidTurn<T>(step: MermaidTurnStep<T>): Promise<T> {
  const turn = last.then(() => withTimeout(step))
  // A failed or timed-out turn must not break the chain for the ones after it.
  last = turn.catch(() => undefined)
  return turn
}
