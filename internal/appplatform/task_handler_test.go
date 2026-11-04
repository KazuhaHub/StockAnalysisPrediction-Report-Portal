package appplatform

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTaskHandlerRegistryNarrowsAppAndCapabilities(t *testing.T) {
	if !ValidBackendRequestRoute("POST", TaskBackendPath) {
		t.Fatal("fixed backend task route is not a valid own-backend route")
	}
	if !ValidBackendRequestRoute("POST", TaskEventPath) {
		t.Fatal("fixed event task route is not a valid own-backend route")
	}
	if path, ok := TaskHandlerPath(TaskHandlerEvent); !ok || path != TaskEventPath {
		t.Fatal("event task route resolution", path, ok)
	}
	if path, ok := TaskHandlerPath(TaskHandlerBackend); !ok || path != TaskBackendPath {
		t.Fatal("backend task route resolution", path, ok)
	}
	granted := map[string]bool{"background.run": true, "tasks.schedule": true, "reports.read": true, "events.subscribe": true}
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
	event, err := ResolveTaskHandler("notes-sample", TaskHandlerEvent, granted)
	if err != nil || event.ID != TaskHandlerEvent {
		t.Fatal("event handler", event, err)
	}
	for _, missing := range []string{"background.run", "tasks.schedule", "reports.read", "events.subscribe"} {
		copy := copyTaskCapabilities(granted)
		copy[missing] = false
		handler := TaskHandlerDigest
		appID := "scheduled-digest"
		if missing == "events.subscribe" {
			handler, appID = TaskHandlerEvent, "notes-sample"
		}
		if _, err := ResolveTaskHandler(appID, handler, copy); !errors.Is(err, ErrTaskHandler) {
			t.Fatal("missing capability", missing)
		}
	}
	for _, handler := range []string{"", "arbitrary", "app.backend.task/../escape", strings.Repeat("x", 65)} {
		if _, err := ResolveTaskHandler("notes-sample", handler, granted); !errors.Is(err, ErrTaskHandler) {
			t.Fatal("unknown handler accepted", handler)
		}
	}
}

func copyTaskCapabilities(source map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key, value := range source {
		out[key] = value
	}
	return out
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
