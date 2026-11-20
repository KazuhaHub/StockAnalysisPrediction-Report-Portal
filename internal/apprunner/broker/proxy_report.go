package broker

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform/proxyforwarder"
)

// What the runner tells Portal about an installation's tunnels (doc 184 section 7):
// an hourly rollup per host, a coalesced refusal row and the day's usage. They are
// plain closed data with no path, body or header: only host names the policy
// matched, refusal codes and counts.

// Report kinds.
const (
	ProxyReportRollup  = "rollup"
	ProxyReportRefusal = "refusal"
	ProxyReportUsage   = "usage"
)

const (
	// ProxyMaxRollupHosts bounds the hosts of one hour's rollup; any further host is
	// counted under ProxyOtherHost.
	ProxyMaxRollupHosts = 64
	// ProxyOtherHost collects the hosts beyond the bound.
	ProxyOtherHost = "*"
	// ProxyUnknownHost stands for a refused name that is not a plain DNS name, so
	// application-chosen text never reaches an audit row.
	ProxyUnknownHost = "-"

	proxyRefusalInterval = time.Minute
	proxyUsageInterval   = 10 * time.Minute
	proxyMaxRefusalKeys  = 64
)

var errNoBudget = errors.New("no budget to read")

// ProxyHostRollup is one host's activity in one hour. BytesIn is upstream to the
// application, BytesOut the other way.
type ProxyHostRollup struct {
	Host     string           `json:"host"`
	Tunnels  int64            `json:"tunnels"`
	BytesIn  int64            `json:"bytes_in"`
	BytesOut int64            `json:"bytes_out"`
	Closes   map[string]int64 `json:"closes"`
}

// ProxyReport is one report. Which fields are present depends on Kind.
type ProxyReport struct {
	Kind  string            `json:"kind"`
	Hour  string            `json:"hour,omitempty"`
	Hosts []ProxyHostRollup `json:"hosts,omitempty"`
	Host  string            `json:"host,omitempty"`
	Code  string            `json:"code,omitempty"`
	Count int64             `json:"count,omitempty"`
	Day   string            `json:"day,omitempty"`
	Bytes int64             `json:"bytes,omitempty"`
	// Budget is the reviewed daily budget the usage is measured against.
	Budget int64 `json:"budget,omitempty"`
}

func (in *ProxyInstall) hourKey() int64 { return in.runner.Now().UTC().Unix() / 3600 }

func (in *ProxyInstall) hostStats(host string) *proxyHostStats {
	hour := in.hourKey()
	hosts := in.stats[hour]
	if hosts == nil {
		hosts = map[string]*proxyHostStats{}
		in.stats[hour] = hosts
	}
	s := hosts[host]
	if s == nil {
		// The overflow row counts toward the bound, so an hour never has more rows than it.
		if len(hosts) >= ProxyMaxRollupHosts-1 && host != ProxyOtherHost {
			host = ProxyOtherHost
			s = hosts[host]
		}
		if s == nil {
			s = &proxyHostStats{closes: map[string]int64{}}
			hosts[host] = s
		}
	}
	return s
}

func (in *ProxyInstall) opened(host string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.hostStats(host).tunnels++
}

func (in *ProxyInstall) closed(host, reason string, bytesIn, bytesOut int64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	s := in.hostStats(host)
	s.bytesIn += bytesIn
	s.bytesOut += bytesOut
	s.closes[reason]++
	if reason == ProxyCloseSNIMismatch {
		in.noteRefusalLocked(host, proxyforwarder.Refusal(ProxyCloseSNIMismatch))
	}
}

func (in *ProxyInstall) noteRefusal(host string, code proxyforwarder.Refusal) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.noteRefusalLocked(host, code)
}

func (in *ProxyInstall) noteRefusalLocked(host string, code proxyforwarder.Refusal) {
	if !plainDNSName(host) || host != toLower(host) {
		host = ProxyUnknownHost
	}
	key := proxyRefusalKey{host: host, code: code}
	c := in.refusals[key]
	if c == nil {
		if len(in.refusals) >= proxyMaxRefusalKeys {
			key.host = ProxyUnknownHost
			c = in.refusals[key]
		}
		if c == nil {
			c = &proxyRefusalCount{}
			in.refusals[key] = c
		}
	}
	c.count++
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// Reports returns what is due at now and forgets it: the rollup of every
// completed hour, each refusal key at most once a minute with its count, and the
// day's usage at most every ten minutes while it changes. It returns nothing
// for an installation that has done nothing.
func (in *ProxyInstall) Reports(now time.Time) []ProxyReport { return in.reports(now, false) }

// Drain returns everything the installation has not reported, whatever the intervals: the hour still open, every
// refusal with a count and the day's usage. It is for an installation that is about to be dropped (its session
// ended, a new generation replaced it, the runner is shutting down), so that what it counted is not lost.
func (in *ProxyInstall) Drain(now time.Time) []ProxyReport { return in.reports(now, true) }

func (in *ProxyInstall) reports(now time.Time, final bool) []ProxyReport {
	// The day's count may be read from the journal, so it is read before the lock is taken.
	in.mu.Lock()
	budgeted := in.auth != nil
	in.mu.Unlock()
	var used int64
	usedErr := errNoBudget
	if budgeted {
		used, usedErr = in.runner.budget.Used(in.id, now.UTC().Unix()/86400)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	var out []ProxyReport
	current := now.UTC().Unix() / 3600
	var hours []int64
	for hour := range in.stats {
		if hour < current || final {
			hours = append(hours, hour)
		}
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i] < hours[j] })
	for _, hour := range hours {
		names := make([]string, 0, len(in.stats[hour]))
		for host := range in.stats[hour] {
			names = append(names, host)
		}
		sort.Strings(names)
		report := ProxyReport{Kind: ProxyReportRollup, Hour: time.Unix(hour*3600, 0).UTC().Format(time.RFC3339)}
		for _, host := range names {
			s := in.stats[hour][host]
			closes := map[string]int64{}
			for reason, n := range s.closes {
				closes[reason] = n
			}
			report.Hosts = append(report.Hosts, ProxyHostRollup{Host: host, Tunnels: s.tunnels, BytesIn: s.bytesIn, BytesOut: s.bytesOut, Closes: closes})
		}
		delete(in.stats, hour)
		if len(report.Hosts) > 0 {
			out = append(out, report)
		}
	}
	keys := make([]proxyRefusalKey, 0, len(in.refusals))
	for key := range in.refusals {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].host != keys[j].host {
			return keys[i].host < keys[j].host
		}
		return keys[i].code < keys[j].code
	})
	for _, key := range keys {
		c := in.refusals[key]
		if c.count > 0 && (final || c.last.IsZero() || now.Sub(c.last) >= proxyRefusalInterval) {
			out = append(out, ProxyReport{Kind: ProxyReportRefusal, Host: key.host, Code: string(key.code), Count: c.count})
			c.count, c.last = 0, now
		} else if c.count == 0 && now.Sub(c.last) >= 10*proxyRefusalInterval {
			delete(in.refusals, key)
		}
	}
	if in.auth != nil && usedErr == nil {
		day := now.UTC().Unix() / 86400
		if used > 0 && (final && used != in.usageSet || (day != in.usageDay || used != in.usageSet) && (in.usageAt.IsZero() || day != in.usageDay || now.Sub(in.usageAt) >= proxyUsageInterval)) {
			out = append(out, ProxyReport{Kind: ProxyReportUsage, Day: time.Unix(day*86400, 0).UTC().Format("2006-01-02"), Bytes: used, Budget: in.auth.policy.DailyBytes})
			in.usageDay, in.usageSet, in.usageAt = day, used, now
		}
	}
	return out
}

// bandwidth is a byte token bucket: the per-installation throughput cap. Waiting
// is on the real clock, because it paces real I/O.
type bandwidth struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newBandwidth(bytesPerSecond int64, now func() time.Time) *bandwidth {
	if bytesPerSecond <= 0 {
		return nil
	}
	rate := float64(bytesPerSecond)
	burst := max(rate/5, 64*1024)
	return &bandwidth{rate: rate, burst: burst, tokens: burst, last: time.Now(), now: now}
}

// wait takes n bytes of budget, sleeping when the bucket is empty. It returns
// false when ctx ended first.
func (b *bandwidth) wait(ctx context.Context, n int) bool {
	if b == nil {
		return ctx.Err() == nil
	}
	b.mu.Lock()
	t := time.Now()
	if elapsed := t.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(b.burst, b.tokens+elapsed*b.rate)
		b.last = t
	}
	b.tokens -= float64(n)
	var pause time.Duration
	if b.tokens < 0 {
		pause = time.Duration(-b.tokens / b.rate * float64(time.Second))
	}
	b.mu.Unlock()
	if pause <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
