package app

// The group statistics' arithmetic (plan §7.5).
//
// Everything here is a pure function over already-loaded members, so it can be tested against hand
// computed fixtures and so a caller cannot accidentally make a permutation depend on the viewing
// mode. The random source is seeded from the group's dates and the metric's name, never from `mode`,
// which is what makes a whole-month custom group return exactly the monthly cell's p (plan §7.5).

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"sort"
	"strings"
)

const (
	// predPermutations is B in the plan's cost estimate. The p-value is (1+#)/(1+B), so it can never
	// be zero: a statistic no permutation reached is still "smaller than 1 in 10001", not "impossible".
	predPermutations = 10000
	// predMatureMinN is the smallest member count a cell may publish. Below it the cell reports n and
	// nothing else, because a spread over twenty names is mostly noise.
	predMatureMinN = 30
	// predRefusedMaxPct caps terminal refusals as a share of members. Real data refusals correlate
	// with losses (ADR 0029 objection 4), so a cell that refuses too much is not representative.
	predRefusedMaxPct = 5.0
	// predMaxRangeMonths and predMaxWeekGroups bound a custom request. They bound the permutation
	// cost and the number of cells, and a request outside them is refused rather than truncated.
	predMaxRangeMonths = 36
	predMaxWeekGroups  = 26
	// predHitUpsideBp is the |upside| floor for the hit-rate subset: a 1 % target and a 1 % move are
	// not a test of directional skill.
	predHitUpsideBp = 500
)

// averageRanks returns 1-based ranks with ties sharing their mean rank. This is the tie convention
// Spearman's rho requires, and it is why ic is computed on ranks rather than on raw values.
func averageRanks(v []float64) []float64 {
	n := len(v)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
	out := make([]float64, n)
	for i := 0; i < n; {
		j := i + 1
		for j < n && v[idx[j]] == v[idx[i]] {
			j++
		}
		rank := float64(i+j-1)/2 + 1
		for k := i; k < j; k++ {
			out[idx[k]] = rank
		}
		i = j
	}
	return out
}

// pearson is the plain correlation. It returns NaN when either series has no variance, which the
// callers turn into a null rather than into a 0 that reads like "no relationship".
func pearson(x, y []float64) float64 {
	n := len(x)
	if n < 2 || n != len(y) {
		return math.NaN()
	}
	var sx, sy float64
	for i := 0; i < n; i++ {
		sx += x[i]
		sy += y[i]
	}
	mx, my := sx/float64(n), sy/float64(n)
	var sxy, sxx, syy float64
	for i := 0; i < n; i++ {
		dx, dy := x[i]-mx, y[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return math.NaN()
	}
	return sxy / math.Sqrt(sxx*syy)
}

// spearman is Pearson on average ranks, which is what makes ties average rather than arbitrary.
func spearman(x, y []float64) float64 {
	if len(x) != len(y) {
		return math.NaN()
	}
	return pearson(averageRanks(x), averageRanks(y))
}

// predSeed derives a permutation seed from the cell's identity and the metric's name. `mode` is
// deliberately absent: a whole-month custom group must reproduce the monthly cell's p exactly.
func predSeed(parts ...string) int64 {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	b, err := hex.DecodeString(hex.EncodeToString(h[:8]))
	if err != nil || len(b) < 8 {
		return 1
	}
	v := int64(binary.BigEndian.Uint64(b))
	if v < 0 {
		v = -v
	}
	if v == 0 {
		return 1
	}
	return v
}

// permP is the label-permutation p-value. stat receives a permutation of the member indices and
// returns the CENTERED statistic (observed minus its null); the p is two-sided about zero, which
// covers both "spread differs from 0" and "hit differs from its base rate".
//
// Shuffling the index vector rather than the values is what keeps the realised excess returns fixed
// in place while the predictions move across members (design §8.3).
func permP(seed int64, observed float64, n int, stat func(perm []int) float64) float64 {
	if n < 2 || math.IsNaN(observed) {
		return math.NaN()
	}
	r := newPredRand(seed)
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	extreme := 0
	absObs := math.Abs(observed)
	for b := 0; b < predPermutations; b++ {
		r.shuffle(perm)
		if v := stat(perm); !math.IsNaN(v) && math.Abs(v) >= absObs {
			extreme++
		}
	}
	return float64(1+extreme) / float64(1+predPermutations)
}

// bhAdjust is Benjamini–Hochberg over one cell's family (its metrics and its slices). NaNs are
// skipped: a metric with no p (a one-armed spread) is not in the family at all, and counting it
// would shrink every other q for no reason.
//
// The result is in input order, so the caller can write q straight back onto the metrics.
func bhAdjust(p []float64) []float64 {
	idx := make([]int, 0, len(p))
	for i, v := range p {
		if !math.IsNaN(v) && v >= 0 && v <= 1 {
			idx = append(idx, i)
		}
	}
	out := make([]float64, len(p))
	for i := range out {
		out[i] = math.NaN()
	}
	if len(idx) == 0 {
		return out
	}
	sort.SliceStable(idx, func(a, b int) bool { return p[idx[a]] < p[idx[b]] })
	m := float64(len(idx))
	minQ := 1.0
	for k := len(idx) - 1; k >= 0; k-- {
		q := p[idx[k]] * m / float64(k+1)
		if q > 1 {
			q = 1
		}
		if q < minQ {
			minQ = q
		}
		out[idx[k]] = minQ
	}
	return out
}

// meanOf returns 0 for an empty slice. Callers only call it on arms they have already checked are
// non-empty, so the zero is never read.
func meanOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// predRand is a xorshift64 generator with a multiply-shift bound. The permutation loops are the
// endpoint's whole cost — 10 000 shuffles per cell per metric — and the standard library's Source
// spends most of a draw on the modulo in Intn. This is neither safe nor general, and does not need
// to be: the seed is deterministic and the output is only ever a p-value.
type predRand struct{ s uint64 }

func newPredRand(seed int64) *predRand {
	s := uint64(seed)
	if s == 0 {
		s = 0x9e3779b97f4a7c15
	}
	return &predRand{s: s}
}

func (r *predRand) next() uint64 {
	r.s ^= r.s << 13
	r.s ^= r.s >> 7
	r.s ^= r.s << 17
	return r.s
}

// below returns a value in [0,n) by Lemire's multiply-shift, valid for n < 2^32.
func (r *predRand) below(n uint64) int {
	return int(((r.next() >> 32) * n) >> 32)
}

func (r *predRand) shuffle(perm []int) {
	for i := len(perm) - 1; i > 0; i-- {
		j := r.below(uint64(i + 1))
		perm[i], perm[j] = perm[j], perm[i]
	}
}
