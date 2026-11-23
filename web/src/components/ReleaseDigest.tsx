import { Tooltip, Typography } from 'antd'

/** How many leading hex characters stand for a digest: enough to tell two releases apart at a glance. */
export const DIGEST_SHORT = 12

/**
 * A release digest (a 64-character SHA-256) in its short form: the first 12 hex characters in monospace, the whole
 * value in a tooltip (hover or keyboard focus) and on a copy button. Every console place that shows a release digest
 * uses this one component. A value an administrator must compare character by character before confirming (a key
 * fingerprint, a typed archive digest) is not a release digest and stays whole.
 */
export default function ReleaseDigest({ value }: { value: string }) {
  return <span className="rp-digest">
    <Tooltip title={value} trigger={['hover', 'focus']}>
      <Typography.Text code tabIndex={0} className="rp-digest__value">{value.slice(0, DIGEST_SHORT)}</Typography.Text>
    </Tooltip>
    <Typography.Text copyable={{ text: value }} />
  </span>
}
