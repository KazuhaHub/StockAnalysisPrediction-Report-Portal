package app

import (
	"bytes"
	"context"
	"testing"
)

func TestPlatformBackendRegistrationRequiresActualReadiness(t *testing.T) {
	s, lease := platformCandidateIntentFixture(t, false)
	before := dumpOf(t, s.st)
	if registration, err := s.preparePlatformBackendRegistration(context.Background(), lease); err == nil {
		registration.Close()
		t.Fatal("unstarted candidate minted registration")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed preparation wrote state")
	}
}
