package broker

import (
	"testing"
	"time"
)

func TestTokenBucket120PerMinuteBurst20(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	b := NewTokenBucket(120, 20, func() time.Time { return now })
	for i := 0; i < 20; i++ {
		if !b.Allow() {
			t.Fatalf("burst call %d refused", i+1)
		}
	}
	if b.Allow() {
		t.Fatal("21st call within the burst was allowed")
	}
	// 120 per minute is one token per 500 ms, exactly.
	now = now.Add(499 * time.Millisecond)
	if b.Allow() {
		t.Fatal("token granted early")
	}
	now = now.Add(time.Millisecond)
	if !b.Allow() || b.Allow() {
		t.Fatal("exactly one token must be available after 500 ms")
	}
	// A full minute of steady calls yields 120 and no more.
	allowed := 0
	for i := 0; i < 240; i++ {
		now = now.Add(250 * time.Millisecond)
		if b.Allow() {
			allowed++
		}
	}
	if allowed != 120 {
		t.Fatalf("steady rate over one minute: %d", allowed)
	}
	// Idle time never banks more than the burst.
	now = now.Add(time.Hour)
	allowed = 0
	for b.Allow() {
		allowed++
		if allowed > 100 {
			break
		}
	}
	if allowed != 20 {
		t.Fatalf("idle refill exceeded the burst: %d", allowed)
	}
}

func TestTokenBucketRefusesInvalidAndClockStepsBack(t *testing.T) {
	if NewTokenBucket(0, 20, time.Now).Allow() || NewTokenBucket(120, 0, time.Now).Allow() {
		t.Fatal("a bucket without rate or burst allowed a call")
	}
	now := time.Unix(1_800_000_000, 0)
	b := NewTokenBucket(120, 1, func() time.Time { return now })
	if !b.Allow() {
		t.Fatal("first call refused")
	}
	now = now.Add(-time.Hour)
	if b.Allow() {
		t.Fatal("a backwards clock refilled the bucket")
	}
	now = now.Add(time.Hour + 500*time.Millisecond)
	if !b.Allow() {
		t.Fatal("bucket did not recover")
	}
}
