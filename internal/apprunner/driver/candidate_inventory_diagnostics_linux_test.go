//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func inventoryClient(body string) *http.Client {
	return daemonClient(transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
}

func TestInventoryRejectionsNameTheEntryAndTheField(t *testing.T) {
	id := strings.Repeat("c", 64)
	entry := func(mounts string) string {
		return `[{"Id":"` + id + `","Mounts":` + mounts + `}]`
	}
	for name, tc := range map[string]struct{ body, want string }{
		"not an array":     {`{}`, "not a JSON array"},
		"malformed id":     {`[{"Id":"short","Mounts":[]}]`, `malformed Id`},
		"no id":            {`[{"Mounts":[]}]`, "(no Id)"},
		"missing mounts":   {`[{"Id":"` + id + `"}]`, "container cccccccccccc: " + "invalid application runtime response: Mounts key missing"},
		"mounts not array": {entry(`{}`), "Mounts is not an array"},
		"unknown type":     {entry(`[{"Type":"image","Source":"/x"}]`), `mount 0: unsupported type "image"`},
		"relative bind":    {entry(`[{"Type":"bind","Source":"rel"}]`), `mount 0: type "bind" source "rel"`},
		"empty volume":     {entry(`[{"Type":"tmpfs"},{"Type":"volume","Source":""}]`), `mount 1: type "volume" source ""`},
		"absent source":    {entry(`[{"Type":"bind"}]`), `source "<absent>"`},
		"tmpfs source":     {entry(`[{"Type":"tmpfs","Source":"/x"}]`), `tmpfs with source "/x"`},
		"duplicate key":    {`[{"Id":"` + id + `","Id":"` + id + `","Mounts":[]}]`, "projection rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readCandidateIPCInventory(context.Background(), inventoryClient(tc.body))
			if err == nil || !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("want ErrInvalidResponse, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q lacks %q", err, tc.want)
			}
			if strings.Contains(err.Error(), id) {
				t.Fatal("full container id leaked into the error")
			}
		})
	}
}

func TestInventoryErrorBoundsTheSource(t *testing.T) {
	long := "rel/" + strings.Repeat("x", 500)
	raw, _ := json.Marshal([]map[string]any{{"Id": strings.Repeat("d", 64), "Mounts": []map[string]any{{"Type": "bind", "Source": long}}}})
	_, err := readCandidateIPCInventory(context.Background(), inventoryClient(string(raw)))
	if !errors.Is(err, ErrInvalidResponse) || len(err.Error()) > 400 {
		t.Fatalf("unbounded or wrong error: %v", err)
	}
}
