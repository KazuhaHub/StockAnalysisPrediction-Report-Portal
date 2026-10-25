import { api, ApiError } from '../api/client'
import type { QuoteResp } from '../api/types'
import { readQuoteRefreshAdvice } from './quoteRefresh'

// Historical fallback has a separate endpoint so existing quote clients cannot mistake a close
// with no observation timestamp for a live snapshot. Only the server can advertise availability.
export async function fetchQuoteWithHistory(url: string): Promise<QuoteResp> {
  try {
    return await api.get<QuoteResp>(url)
  } catch (error) {
    const details = error instanceof ApiError && error.code === 'quote_unavailable' ? error.data : null
    if (!url.startsWith('/api/quote/') || !details || typeof details !== 'object' || !('historyAvailable' in details) || details.historyAvailable !== true) throw error
    try {
      const history = await api.get<QuoteResp>(url.replace('/api/quote/', '/api/quote-history/'))
      if (history.historyOnly === true) {
        const advice = readQuoteRefreshAdvice(details)
        return { ...history, refreshAfterSecs: advice?.refreshAfterSecs, refreshAt: advice?.refreshAt }
      }
    } catch {
      // Preserve the primary error, including its server-owned retry advice.
    }
    throw error
  }
}
