import { useEffect, useState } from 'react'

/** The current time, read again every `intervalMs`: a page left open re-renders what depends on the clock (a badge that lapses). */
export function useNow(intervalMs = 60_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return now
}
