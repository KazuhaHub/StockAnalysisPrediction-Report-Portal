import { useEffect, useState } from 'react'

/**
 * The page-level "render once" gate. Give it whether every request that feeds the view's first paint has answered
 * (an answer of any kind: a failure settles the page too, with an inline retry, never an endless spinner). It is true
 * from the first moment that holds, and stays true: a later refresh of the same data does not put the spinner back.
 * A view that is not settled shows its one page-level loading state; once settled it renders complete, so no name,
 * label or card appears or changes afterwards because a second request arrived.
 */
export function useSettled(answered: boolean): boolean {
  const [settled, setSettled] = useState(false)
  useEffect(() => { if (answered) setSettled(true) }, [answered])
  return settled || answered
}
