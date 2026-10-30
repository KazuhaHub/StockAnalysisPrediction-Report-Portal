package app

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestPlatformWritableRequestBindingBoundary(t *testing.T) {
	s := &Server{}
	valid := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64)}
	for _, change := range []func(*platformBackendRequestBinding){
		func(b *platformBackendRequestBinding) { b.Generation = "" },
		func(b *platformBackendRequestBinding) { b.Generation = "01" },
		func(b *platformBackendRequestBinding) { b.Generation = "9223372036854775808" },
		func(b *platformBackendRequestBinding) { b.PolicyRevision = "0" },
		func(b *platformBackendRequestBinding) { b.PolicyRevision = "+1" },
		func(b *platformBackendRequestBinding) { b.ReleaseSHA256 = strings.Repeat("A", 64) },
		func(b *platformBackendRequestBinding) { b.ReleaseSHA256 = "" },
	} {
		b := valid
		change(&b)
		r, err := s.requestPlatformBoundWritableBackend(context.Background(), platformActorSession{}, platformFixtureID(11), b, "GET", "/v1/echo", nil, platformRuntimeEndpoint{})
		if !errors.Is(err, errAppPlatformLaunchUnavailable) || len(r.Body) != 0 || errors.Is(err, errAppPlatformBackendRequestUncertain) {
			t.Fatal("invalid binding reached storage or dispatch", err)
		}
	}
}

func testPlatformWritableRequestBinding(t *testing.T, pg bool) {
	s, actor, lease := platformWritableAccessFixture(t, pg)
	a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	valid := platformBackendRequestBinding{Generation: "1", PolicyRevision: strconv.FormatInt(a.policy, 10), ReleaseSHA256: platformDigest(a.intent.start.writer.descriptor)}
	a.Close()
	before := dumpOf(t, s.st)
	for _, field := range []string{"generation", "policy", "release"} {
		t.Run(field, func(t *testing.T) {
			b := valid
			switch field {
			case "generation":
				b.Generation = "2"
			case "policy":
				b.PolicyRevision = "2"
			case "release":
				b.ReleaseSHA256 = strings.Repeat("0", 64)
			}
			// The absent endpoint would produce a different error if root IO
			// were reached. Binding rejection must happen in current access.
			r, err := s.requestPlatformBoundWritableBackend(context.Background(), actor, lease.installationID, b, "POST", "/v1/echo", []byte(`{"write":true}`), platformRuntimeEndpoint{})
			if !errors.Is(err, errAppPlatformLaunchUnavailable) || errors.Is(err, errAppPlatformBackendRequestUncertain) || len(r.Body) != 0 {
				t.Fatal("stale page binding not refused before dispatch", err)
			}
		})
	}
	if after := dumpOf(t, s.st); !bytes.Equal(stripHeader(before), stripHeader(after)) {
		t.Fatal("binding refusal mutated storage")
	}
	// A matching binding admits the current source, but does not manufacture
	// native readiness or a successful response from SQL fixture metadata.
	r, err := s.requestPlatformBoundWritableBackend(context.Background(), actor, lease.installationID, valid, "GET", "/v1/echo", nil, platformRuntimeEndpoint{})
	if err == nil || errors.Is(err, errAppPlatformLaunchUnavailable) || errors.Is(err, errAppPlatformBackendRequestUncertain) || len(r.Body) != 0 {
		t.Fatal("current binding did not reach the independent native gate", err)
	}
}

func TestPlatformWritableRequestBindingSQLite(t *testing.T) {
	testPlatformWritableRequestBinding(t, false)
}

func TestPlatformWritableRequestBindingPostgres(t *testing.T) {
	testPlatformWritableRequestBinding(t, true)
}
