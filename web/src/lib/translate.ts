/**
 * The translate function as this app's helpers take it: a key alone, or a key with interpolation
 * values. It is the shape of i18next's `t` from `useTranslation()`, written as the same two
 * overloads so that `t` itself satisfies it, and so does a test double such as `(k) => k`.
 *
 * One definition on purpose. Each helper used to declare its own single-signature type
 * (`(k: string, o?: Record<string, unknown>) => string` and five variants), and i18next 27's
 * overloaded `TFunction` matches none of them. A helper that forwards optional values calls
 * `t(key)` or `t(key, values)` explicitly: `undefined` is not a set of values.
 */
export interface Translate {
  (key: string): string
  (key: string, values: Record<string, unknown>): string
}
