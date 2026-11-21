import { Typography } from 'antd'

const tailLength = 8

/**
 * A long machine identifier (a UUID, a key fingerprint, a hash): monospace on one line, shortened
 * in the middle when it does not fit, so neither its start nor its last characters are lost and it
 * never wraps a lone character onto its own line. The whole value is in the tooltip and on the copy
 * button (.rp-id in index.css). It fills a box whose width its surroundings set (a Descriptions item,
 * a stretched Flex child); it is not for running text or shrink-to-fit boxes.
 */
export default function LongId({ value }: { value: string }) {
  const head = value.length > tailLength ? value.slice(0, -tailLength) : ''
  const tail = value.length > tailLength ? value.slice(-tailLength) : value
  return <Typography.Text className="rp-id" copyable={{ text: value }}>
    <span className="rp-id__value" title={value}><span className="rp-id__head">{head}</span><span className="rp-id__tail">{tail}</span></span>
  </Typography.Text>
}
