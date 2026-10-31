package appplatform

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTaskHandlerRegistryNarrowsAppAndCapabilities(t *testing.T) {
	granted := map[string]bool{"background.run": true, "tasks.schedule": true, "reports.read": true}
	backend, err := ResolveTaskHandler("notes-sample", TaskHandlerBackend, granted)
	if err != nil || backend.ID != TaskHandlerBackend {
		t.Fatal("backend handler", backend, err)
	}
	if _, err := ResolveTaskHandler("notes-sample", TaskHandlerDigest, granted); !errors.Is(err, ErrTaskHandler) {
		t.Fatal("digest handler escaped app binding")
	}
	digest, err := ResolveTaskHandler("scheduled-digest", TaskHandlerDigest, granted)
	if err != nil || digest.AllowedAppID != "scheduled-digest" {
		t.Fatal("digest handler", digest, err)
	}
	for _, missing := range []string{"background.run", "tasks.schedule", "reports.read"} {
		copy := map[string]bool{"background.run": true, "tasks.schedule": true, "reports.read": true}
		copy[missing] = false
		if _, err := ResolveTaskHandler("scheduled-digest", TaskHandlerDigest, copy); !errors.Is(err, ErrTaskHandler) {
			t.Fatal("missing capability", missing)
		}
	}
	for _, handler := range []string{"", "arbitrary", "app.backend.task/../escape", strings.Repeat("x", 65)} {
		if _, err := ResolveTaskHandler("notes-sample", handler, granted); !errors.Is(err, ErrTaskHandler) {
			t.Fatal("unknown handler accepted", handler)
		}
	}
}

func TestTaskPayloadIsCanonicalBoundedAndClosed(t *testing.T) {
	canonical, digest, err := ValidateTaskPayload([]byte(`{"scheduleId":"daily","count":2}`))
	if err != nil || string(canonical) != `{"count":2,"scheduleId":"daily"}` || !strings.HasPrefix(digest, "") || len(digest) != 64 {
		t.Fatal("canonical payload", string(canonical), digest, err)
	}
	again, againDigest, err := ValidateTaskPayload(canonical)
	if err != nil || !bytes.Equal(again, canonical) || againDigest != digest {
		t.Fatal("payload digest is not stable")
	}
	for _, bad := range []string{
		`[]`, `null`, `{} {}`, `{"a":1,"a":2}`,
		`{"url":"https://example.invalid"}`, `{"nested":{"callback":"x"}}`,
		`{"items":[{"token":"secret"}]}`, `{"path":"/tmp/escape"}`,
		`{"value":` + strings.Repeat("1", 8193) + `}`,
		`{"text":"` + strings.Repeat("x", 4097) + `"}`,
	} {
		if _, _, err := ValidateTaskPayload([]byte(bad)); !errors.Is(err, ErrTaskHandler) {
			t.Fatal("unsafe payload accepted", bad[:min(len(bad), 80)])
		}
	}
}
