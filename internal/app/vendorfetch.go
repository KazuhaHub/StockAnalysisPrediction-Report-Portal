package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Outbound fetches to the Chinese market-data vendors: Tencent (qt.gtimg.cn, web.ifzq.gtimg.cn),
// Sina (hq.sinajs.cn, money.finance.sina.com.cn) and eastmoney (push2.eastmoney.com). Both the
// company-name fetch and the quote fetch come through here, because each of them had independently
// got some part of it wrong.
//
// This is deliberately NOT newSafeClient (safefetch.go). That client exists because SSO fetches an
// admin-supplied URL and must not be talked into hitting the cloud metadata endpoint; every host
// reached from here is a compile-time constant, so its address policy buys nothing — while its
// complete absence of request headers would break these endpoints outright, since Sina answers 403
// to a request that arrives without a Referer.

const (
	// These endpoints are the ones the vendor's own quote page calls, and they answer Go's default
	// agent inconsistently — an empty body on some hosts, an interstitial on others.
	vendorUserAgent = "Mozilla/5.0"

	// A backstop, not the real budget: every caller passes its own, tighter, context deadline. It
	// is here so that a caller which hands over context.Background() still cannot park a goroutine
	// on a vendor that accepts the connection and then says nothing.
	vendorClientTimeout = 30 * time.Second
)

// vendorClient is ONE pooled client for every vendor call in the process. The quote endpoint is hit
// once per stock page view, and what this replaced built a fresh http.Client — therefore a fresh
// Transport, therefore a fresh, private, never-reused connection pool — on every single fetch, so
// each quote paid for a TCP handshake plus a TLS handshake that the previous one had already paid
// for. MaxIdleConnsPerHost is raised off the stdlib default of 2 because this traffic is
// concentrated on a handful of hosts, which is precisely the shape that default is wrong for.
var vendorClient = &http.Client{
	Timeout: vendorClientTimeout,
	Transport: &vendorScheduledTransport{scheduler: publicVendorScheduler, base: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}},
}

// vendorGet GETs a vendor URL under ctx's deadline and returns at most limit bytes of the body.
func vendorGet(ctx context.Context, rawURL, referer string, limit int64) ([]byte, error) {
	// The error is returned, never dropped into `req, _ :=`. A six-byte stock symbol carrying a
	// control byte arrives here from ingest; http.NewRequest refuses a control character in a URL
	// and hands back a nil request, and the line that then set a header on it dereferenced that
	// nil — inside the bare goroutine in Names.Resolve, which the only recover in the tree
	// (batch_run.go) cannot see, so it took the whole process down.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if source := vendorSourceForHost(req.URL.Hostname()); source != "" {
		req = req.WithContext(context.WithValue(req.Context(), vendorOriginKey{}, source))
	}
	req.Header.Set("User-Agent", vendorUserAgent)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := vendorHTTPClient(ctx).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Refusing the status is the reason this function exists. A deployment whose egress is
		// blocked gets an HTML interstitial with a 403 on it; GBK-decoding that and running the
		// name or quote parser over it yields the empty string — the very same value a connection
		// refusal yields — so the second source is consulted, also fails the same way, and the
		// caller reports "this stock has no name" instead of "we cannot reach any vendor". Nothing
		// is logged and nothing is retried: the two-source failover is decorative until the status
		// is checked here.
		//
		// The response is read for EVIDENCE before it is drained, because a status alone cannot
		// tell a WAF from a rate limit from an endpoint that moved: on 2026-10-24 one deployment saw
		// 501 from the settlement endpoint and 403 from the display one, and the only way to tell
		// "we are being throttled" from "this URL is gone" was a header and the start of the body —
		// both of which this function used to discard.
		return nil, vendorStatusFrom(req, resp, limit)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// vendorBodySummaryBytes bounds what is read off a FAILED response to describe it. The caller's own
// limit bounds the success body and is usually far larger; this one is a log line's worth.
const vendorBodySummaryBytes = 2048

// vendorBodySummaryRunes bounds the summary itself. Runes, not bytes: the body is usually Chinese and
// a byte cap would cut a character in half.
const vendorBodySummaryRunes = 120

// vendorStatusError is a non-2xx vendor answer, carrying what an operator needs to tell apart the
// cases that all look identical as a bare status code: a WAF challenge (`Server`, a short HTML page),
// a rate limit (429, or `Retry-After`), and an endpoint that moved (404/410/501).
type vendorStatusError struct {
	Host        string
	Method      string
	RequestPath string
	StatusCode  int
	Status      string
	Proto       string
	Server      string
	RetryAfter  string
	Body        string
}

func (e *vendorStatusError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "vendor %s returned %s (%s %s, %s", e.Host, e.Status, e.Method, e.RequestPath, e.Proto)
	if e.Server != "" {
		fmt.Fprintf(&b, ", Server: %s", e.Server)
	}
	if e.RetryAfter != "" {
		fmt.Fprintf(&b, ", Retry-After: %s", e.RetryAfter)
	}
	b.WriteString(")")
	if e.Body != "" {
		b.WriteString(": ")
		b.WriteString(e.Body)
	}
	return b.String()
}

// vendorStatusFrom reads a bounded, cleaned summary off a failed response and returns the typed
// error. The rest of the body is drained so the connection returns to the pool either way — the
// drain is what has to happen, and the summary is taken from the front of it rather than in place
// of it.
func vendorStatusFrom(req *http.Request, resp *http.Response, limit int64) error {
	bound := int64(vendorBodySummaryBytes)
	if limit > 0 && limit < bound {
		bound = limit
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, bound))
	// Only what is LEFT of the caller's budget: the summary bytes are part of that budget, not an
	// addition to it, so a failed response never costs more than a successful one.
	if limit > bound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, limit-bound))
	}
	return &vendorStatusError{
		Host:        req.URL.Host,
		Method:      req.Method,
		RequestPath: req.URL.Path,
		StatusCode:  resp.StatusCode,
		Status:      resp.Status,
		Proto:       resp.Proto,
		Server:      resp.Header.Get("Server"),
		RetryAfter:  resp.Header.Get("Retry-After"),
		Body:        vendorBodySummary(raw),
	}
}

// vendorBodySummary turns a few hundred bytes of somebody else's error page into one short clause:
// markup stripped, whitespace collapsed, cut to vendorBodySummaryRunes. It is NOT GBK-decoded — this
// is evidence for a developer reading a log, and the ASCII around the Chinese (the code, "rate
// limited", a redirect target) is what identifies the case.
func vendorBodySummary(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	text := strings.Join(strings.Fields(vendorTagRE.ReplaceAllString(string(raw), " ")), " ")
	if runes := []rune(text); len(runes) > vendorBodySummaryRunes {
		text = string(runes[:vendorBodySummaryRunes]) + "…"
	}
	return text
}

// vendorTagRE matches an HTML tag. Deliberately crude: this runs on error bodies, a body that survives
// with a little noise is still readable evidence, and a strict parser would add a dependency and a
// failure mode to the one path that has already gone wrong.
var vendorTagRE = regexp.MustCompile(`<[^>]*>`)

// vendorGetGBK is vendorGet plus the GBK to UTF-8 decode these endpoints need: Sina's snapshot line
// and Tencent's realtime line are both GBK, and Chinese company names come out as mojibake without it.
func vendorGetGBK(ctx context.Context, rawURL, referer string, limit int64) (string, error) {
	body, err := vendorGet(ctx, rawURL, referer, limit)
	if err != nil {
		return "", err
	}
	// Decoding bytes that are already truncated, instead of wrapping the live body in the decoder
	// and reading that to EOF, is what makes the bound real: a transform.Reader over an unbounded
	// body is still unbounded, which is how the previous code managed to have a limit nowhere at
	// all. GBK never expands past 1.5x on the way to UTF-8, so the result is bounded too.
	out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), body)
	if err != nil {
		return "", fmt.Errorf("decode GBK from %s: %w", vendorHost(rawURL), err)
	}
	return string(out), nil
}

// vendorNow is time.Now behind a var so a test can move the log limiter's clock instead of sleeping
// through a real minute.
var vendorNow = time.Now

// vendorLogInterval is the most often one host may put a line in the log.
const vendorLogInterval = time.Minute

// vendorLogLimiter is how a vendor outage leaves evidence without drowning the log. The name fetch
// runs on every ingest and the quote fetch on every page view, so logging each failure would bury
// the log under thousands of copies of a single fact — and logging none of them is exactly how a
// silent failover survived into production in the first place. One line per host per minute is the
// compromise that keeps both from happening.
type vendorLogLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

var vendorLogs vendorLogLimiter

// allow reports whether host may log at now, recording the decision it just made.
func (l *vendorLogLimiter) allow(host string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if at, ok := l.last[host]; ok && now.Sub(at) < vendorLogInterval {
		return false
	}
	// The keys are vendor hostnames — a handful of compile-time constants — so this map does not
	// grow in practice. The sweep is here so that a future caller reaching a more varied set of
	// hosts cannot quietly turn a log limiter into a memory leak.
	if len(l.last) > 64 {
		for k, at := range l.last {
			if now.Sub(at) >= vendorLogInterval {
				delete(l.last, k)
			}
		}
	}
	l.last[host] = now
	return true
}

// vendorLogf logs one line about rawURL's host, at most once per vendorLogInterval per host. Vendor
// URLs carry no credential — unlike the geo database URL in geo_update.go, which has a licence key
// in its query string and has to be redacted — so the host goes into the log as it is.
func vendorLogf(rawURL, format string, args ...any) {
	host := vendorHost(rawURL)
	if !vendorLogs.allow(host, vendorNow()) {
		return
	}
	log.Printf("quote vendor %s: %s", host, fmt.Sprintf(format, args...))
}

// vendorHost names the host to blame in a log line. A URL that will not even parse — which is
// precisely the case that used to crash the process — has no host of its own, so it gets one
// bucket rather than being dropped from the log entirely.
func vendorHost(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "(unparseable url)"
}
