import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'

/**
 * The last item of the shell's breadcrumb, supplied by the page it names. The shell knows the route (Home / Apps) but not
 * an app's name; the page learns the name with the data that fills its first paint, and hands it up in the same render,
 * so the breadcrumb and the page appear together: Home / Apps / Market Monitor.
 *
 * `undefined` is "not known yet": the shell shows no breadcrumb for such a route (it never shows the shorter trail and
 * grows it later). A string is the name. `null` is "settled without one" (the page failed to load): the trail ends at its
 * parent.
 */
interface CrumbTailValue {
  tail: string | null | undefined
  setTail: (tail: string | null | undefined) => void
}

const Ctx = createContext<CrumbTailValue | null>(null)

export function CrumbTailProvider({ children }: { children: ReactNode }) {
  const [tail, setTail] = useState<string | null | undefined>(undefined)
  const value = useMemo(() => ({ tail, setTail }), [tail])
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

/** The tail the page registered, for the shell. */
export function useCrumbTailValue(): string | null | undefined {
  return useContext(Ctx)?.tail
}

/** A page names the last breadcrumb item once its first-paint data is in; it is forgotten when the page goes away. */
export function useCrumbTail(label: string | null | undefined): void {
  const setTail = useContext(Ctx)?.setTail
  useEffect(() => {
    setTail?.(label)
  }, [setTail, label])
  useEffect(() => () => setTail?.(undefined), [setTail])
}
