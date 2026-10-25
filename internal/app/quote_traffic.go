package app

import "time"

// Fixed buckets bound memory regardless of process lifetime or symbol cardinality. Percentiles
// are bucket upper bounds; zero means no samples. The final overflow bucket uses the observed max.
var quoteLatencyBounds = [...]int64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000}

type quoteLatencyHistogram struct {
	buckets [len(quoteLatencyBounds) + 1]uint64
	count   uint64
	maximum int64
}

func (h *quoteLatencyHistogram) observe(duration time.Duration) {
	ms := max(0, duration.Milliseconds())
	i := 0
	for i < len(quoteLatencyBounds) && ms > quoteLatencyBounds[i] {
		i++
	}
	h.buckets[i]++
	h.count++
	h.maximum = max(h.maximum, ms)
}

func (h *quoteLatencyHistogram) p95Millis() int64 {
	if h.count == 0 {
		return 0
	}
	threshold := h.count - h.count/20
	var cumulative uint64
	for i, count := range h.buckets {
		cumulative += count
		if cumulative >= threshold {
			if i < len(quoteLatencyBounds) {
				return quoteLatencyBounds[i]
			}
			return h.maximum
		}
	}
	return 0
}

type quoteRequestTraffic struct {
	Requests  uint64 `json:"requests"`
	Completed uint64 `json:"completed"`
	CacheHits uint64 `json:"cacheHits"`
	Merged    uint64 `json:"merged"`
	Loads     uint64 `json:"loads"`
	Failures  uint64 `json:"failures"`
	Stale     uint64 `json:"stale"`
	P95Millis int64  `json:"p95Millis"`
	latency   quoteLatencyHistogram
}

type quoteBatchTraffic struct {
	Requests       uint64 `json:"requests"`
	Completed      uint64 `json:"completed"`
	Symbols        uint64 `json:"symbols"`
	CachedSymbols  uint64 `json:"cachedSymbols"`
	MissingSymbols uint64 `json:"missingSymbols"`
	Merged         uint64 `json:"merged"`
	Loads          uint64 `json:"loads"`
	P95Millis      int64  `json:"p95Millis"`
	latency        quoteLatencyHistogram
}

type quoteTraffic struct {
	Live    quoteRequestTraffic `json:"live"`
	History quoteRequestTraffic `json:"history"`
	Batch   quoteBatchTraffic   `json:"batch"`
}

type quoteRequestDisposition int

const (
	quoteRequestCache quoteRequestDisposition = iota
	quoteRequestMerged
	quoteRequestLoad
)

func (c *quoteCache) requestTrafficLocked(history bool) *quoteRequestTraffic {
	if history {
		return &c.traffic.History
	}
	return &c.traffic.Live
}

func (c *quoteCache) startQuoteRequest(history bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestTrafficLocked(history).Requests++
}

func (c *quoteCache) finishQuoteRequest(history bool, disposition quoteRequestDisposition, response *QuoteResp, err error, elapsed time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := c.requestTrafficLocked(history)
	stats.Completed++
	stats.latency.observe(elapsed)
	switch disposition {
	case quoteRequestCache:
		stats.CacheHits++
	case quoteRequestMerged:
		stats.Merged++
	case quoteRequestLoad:
		stats.Loads++
	}
	if err != nil {
		stats.Failures++
	}
	if response != nil && response.Stale {
		stats.Stale++
	}
}

func (c *quoteCache) finishBatchRequest(symbols, cached, returned int, elapsed time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := &c.traffic.Batch
	stats.Completed++
	stats.Symbols += uint64(symbols)
	stats.CachedSymbols += uint64(cached)
	stats.MissingSymbols += uint64(max(0, symbols-returned))
	stats.latency.observe(elapsed)
}

func (c *quoteCache) recordBatchLoad(merged bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if merged {
		c.traffic.Batch.Merged++
	} else {
		c.traffic.Batch.Loads++
	}
}

func (c *quoteCache) trafficSnapshot() quoteTraffic {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := c.traffic
	copy.Live.P95Millis = copy.Live.latency.p95Millis()
	copy.History.P95Millis = copy.History.latency.p95Millis()
	copy.Batch.P95Millis = copy.Batch.latency.p95Millis()
	return copy
}
