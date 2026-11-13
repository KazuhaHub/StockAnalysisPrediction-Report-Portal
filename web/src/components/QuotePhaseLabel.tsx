import { theme } from 'antd'
import { useTranslation } from 'react-i18next'
import type { CardQuote } from '../lib/useHomeQuotes'

// Phase -> locale key for the label beside a home price. Trading and closed reuse the reading
// page's session keys so the two surfaces cannot word one state differently. A phase not in this
// table (unknown, or one a newer server invents) prints nothing: no label is honest, a wrong one is
// not.
export const PHASE_LABEL_KEYS: Record<string, string> = {
  pre_open: 'quote.phase.pre_open',
  trading: 'quote.session.open',
  lunch_break: 'quote.phase.lunch_break',
  closed: 'quote.session.close',
  holiday: 'quote.phase.holiday',
}

/**
 * The label that says what a home card's number is, or null when there is nothing honest to say.
 *
 * The "previous close" suffix is a claim about the NUMBER, and it is only true while the price has
 * not moved: the vendor's call auction and Hong Kong's pre-market both publish an indicative last
 * before the bell, so a pre-open card with a non-zero change is not showing the previous close and
 * must not say so. The test is the integer 分 change, never the percentage string.
 *
 * Colourless on purpose, in the theme's secondary text colour: green on these cards is a FALLING
 * price, so a coloured label beside a red number would read as part of the same colour language.
 * It ellipsizes rather than clipping mid-glyph, and the price spans beside it must not shrink.
 */
export function QuotePhaseLabel({ quote }: { quote: CardQuote }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const base = PHASE_LABEL_KEYS[quote.phase]
  if (!base) return null
  const key = quote.phase === 'pre_open' && Math.trunc(quote.change) === 0 ? 'quote.phase.pre_open_prev_close' : base
  return (
    <span
      data-testid="card-quote-phase"
      style={{
        color: token.colorTextSecondary,
        fontSize: 12,
        lineHeight: 1.2,
        whiteSpace: 'nowrap',
        minWidth: 0,
        flexShrink: 1,
        overflow: 'hidden',
        textOverflow: 'ellipsis',
      }}
    >
      {t(key)}
    </span>
  )
}
