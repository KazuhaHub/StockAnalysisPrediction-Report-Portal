package broker

import (
	"bytes"
	"encoding/base64"
	"math/rand"
	"strings"
	"testing"
)

// The guard holds back only a suffix that is a proper prefix of a recognised
// form (a match that could still be completed). Ordinary events are released as
// they arrive, not one chunk late.
func TestStreamEchoGuardReleasesAnEventWithNoPartialMatchPending(t *testing.T) {
	value := []byte("Bearer sk-0123456789abcdefghijklmnopqrstuvwxyz")
	g := newHTTPStreamEchoGuard("Authorization", value)
	for _, event := range []string{"[1]\n", "{}\n", `{"a":1}` + "\n", "data: hi\n\n"} {
		got, hit := g.Accept([]byte(event))
		if hit || string(got) != event {
			t.Fatalf("event %q delayed or blocked: released %q hit=%v", event, got, hit)
		}
	}
	if rest := g.Flush(); len(rest) != 0 {
		t.Fatalf("flush had %q left over", rest)
	}
}

// A suffix that still could become the credential is held, and released the
// moment it cannot: here "B" (and "Bear") begin the header form.
func TestStreamEchoGuardHoldsOnlyAPossibleMatch(t *testing.T) {
	value := []byte("Bearer sk-0123456789abcdefghijklmnopqrstuvwxyz")
	g := newHTTPStreamEchoGuard("Authorization", value)
	got, hit := g.Accept([]byte("hello Bear"))
	if hit || string(got) != "hello " {
		t.Fatalf("released %q hit=%v, want only the part that cannot match", got, hit)
	}
	got, hit = g.Accept([]byte("ing news\n"))
	if hit || string(got) != "Bearing news\n" {
		t.Fatalf("held bytes were not released once they could not match: %q hit=%v", got, hit)
	}
	// The held prefix completes into a match: blocked, and nothing of it leaks.
	g = newHTTPStreamEchoGuard("Authorization", value)
	got, _ = g.Accept([]byte("x " + string(value[:20])))
	if bytes.Contains(got, value[:3]) {
		t.Fatalf("a possible match was released: %q", got)
	}
	if _, hit = g.Accept(append(bytes.Clone(value[20:]), '\n')); !hit {
		t.Fatal("the completed match was not caught")
	}
}

// Whatever the chunking, the guard releases exactly the bytes it was given when
// no form of the credential occurs, and never anything once one does.
func TestStreamEchoGuardPreservesTheStreamAndCatchesEveryChunking(t *testing.T) {
	value := []byte("Bearer sk-0123456789abcdefghijklmnopqrstuvwxyz")
	rng := rand.New(rand.NewSource(7))
	clean := []byte(strings.Repeat(`data: {"n":1,"w":"Bear sk-012 Be"}`+"\n\n", 40))
	dirty := append(append(bytes.Clone(clean), []byte(base64.StdEncoding.EncodeToString([]byte("p"+string(value[7:]))))...), clean...)
	for trial := 0; trial < 200; trial++ {
		cut := func(data []byte) [][]byte {
			var out [][]byte
			for len(data) > 0 {
				n := min(len(data), 1+rng.Intn(70))
				out = append(out, data[:n])
				data = data[n:]
			}
			return out
		}
		g := newHTTPStreamEchoGuard("Authorization", value)
		var released []byte
		for _, c := range cut(clean) {
			got, hit := g.Accept(c)
			if hit {
				t.Fatal("false positive")
			}
			released = append(released, got...)
		}
		released = append(released, g.Flush()...)
		if !bytes.Equal(released, clean) {
			t.Fatal("the guard changed the stream")
		}
		g = newHTTPStreamEchoGuard("Authorization", value)
		caught := false
		var out []byte
		for _, c := range cut(dirty) {
			got, hit := g.Accept(c)
			out = append(out, got...)
			if hit {
				caught = true
				break
			}
		}
		if !caught {
			t.Fatal("an echo escaped under some chunking")
		}
		if bytes.Contains(out, []byte(base64.StdEncoding.EncodeToString([]byte("p" + string(value[7:])))[:12])) {
			t.Fatal("part of the echo was released")
		}
	}
}

// Transformed forms are looked for only up to a bound; a longer value is matched
// as it is and case-folded, so the hold and the work stay bounded.
func TestEchoMatcherBoundsTransformedForms(t *testing.T) {
	long := bytes.Repeat([]byte("k"), echoTransformMaxBytes+1)
	m := newEchoMatcher("X-API-Key", long)
	defer m.Clear()
	if !m.Match(append([]byte("x"), long...)) || !m.Match(bytes.ToUpper(long)) {
		t.Fatal("a long value is no longer matched as it is")
	}
	if m.Match([]byte(base64.StdEncoding.EncodeToString(long))) {
		t.Fatal("a transformed form of a value over the bound was looked for")
	}
	if m.span > 2*len(long) {
		t.Fatalf("span %d grew with transforms", m.span)
	}
	within := bytes.Repeat([]byte("k"), echoTransformMaxBytes)
	n := newEchoMatcher("X-API-Key", within)
	defer n.Clear()
	if !n.Match([]byte(base64.StdEncoding.EncodeToString(within))) {
		t.Fatal("a value at the bound lost its transformed forms")
	}
	// No form is held twice in a list.
	for _, list := range [][][]byte{n.exact, n.folded} {
		seen := map[string]bool{}
		for _, f := range list {
			if seen[string(f)] {
				t.Fatal("duplicate form")
			}
			seen[string(f)] = true
		}
	}
}
