import { useEffect, useState } from 'react'

/**
 * `flag`, but true only once it has stayed true for `ms`: a loading state that ends sooner never
 * shows, so a fast answer does not flash a placeholder [Primer loading].
 */
export default function useDelayedFlag(flag: boolean, ms = 300) {
  const [shown, setShown] = useState(false)
  useEffect(() => {
    if (!flag) {
      setShown(false)
      return
    }
    const timer = setTimeout(() => setShown(true), ms)
    return () => clearTimeout(timer)
  }, [flag, ms])
  return flag && shown
}
