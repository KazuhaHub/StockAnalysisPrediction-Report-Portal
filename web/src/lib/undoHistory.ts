// A bounded undo/redo stack. push records the state from before an operation; undo and redo take the
// current state, store it on the opposite stack and return the state to restore, or undefined when
// there is nothing left. The oldest entry is dropped once the stack passes its limit.
export class UndoHistory<T> {
  private past: T[] = []
  private future: T[] = []

  constructor(private readonly limit: number) {}

  push(before: T) {
    this.past.push(before)
    if (this.past.length > this.limit) this.past.shift()
    this.future = []
  }

  undo(current: T): T | undefined {
    const snap = this.past.pop()
    if (snap !== undefined) this.future.push(current)
    return snap
  }

  redo(current: T): T | undefined {
    const snap = this.future.pop()
    if (snap !== undefined) this.past.push(current)
    return snap
  }

  clear() {
    this.past = []
    this.future = []
  }
}
