//go:build linux || darwin

package journal

import (
	"context"
	"testing"
)

// BenchmarkWritableRelayJournalWork is the journal's share of one relayed request: two native steps, each a
// preflight transaction before the native IO and a postflight transaction after it (the same reads, claims and
// currency checks finishWritable* make, without the native observation they compare against).
func BenchmarkWritableRelayJournalWork(b *testing.B) {
	t := &testing.T{}
	s, raw, sig, before := writableHandshakeFixture(t)
	defer s.Close()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for step := 0; step < 2; step++ {
			pre, err := s.prepareWritableBackendExchange(ctx, raw, sig, nil)
			if err != nil {
				b.Fatal(err)
			}
			tx, f, err := s.beginWritableRegistration(ctx, pre.facts.raw, pre.facts.signature, "applied")
			if err != nil {
				b.Fatal(err)
			}
			if !sameWritableRegistrationSource(before.facts, f) {
				b.Fatal("source drifted")
			}
			if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
				b.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
				b.Fatal(err)
			}
		}
	}
}
