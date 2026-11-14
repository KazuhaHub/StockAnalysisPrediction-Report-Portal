//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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
		"relative volume":  {entry(`[{"Type":"tmpfs"},{"Type":"volume","Source":"rel"}]`), `mount 1: type "volume" source "rel"`},
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

// The payload is what Docker 28.0.4 (API 1.48) returned on a GitHub-hosted
// runner for a postgres:16-alpine service container: the list endpoint reports
// an anonymous volume with an empty Source, while the inspect endpoint reports
// its path under the Docker root.
func TestInventoryAdmitsDockerListedAnonymousVolume(t *testing.T) {
	raw, err := os.ReadFile("testdata/docker28-postgres-service-containers.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := readCandidateIPCInventory(context.Background(), inventoryClient(string(raw)))
	if err != nil {
		t.Fatalf("a foreign container with an anonymous volume refused the inventory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries %d", len(entries))
	}
	data := t.TempDir()
	if err := requireNoContainerMountReferences(context.Background(), inventoryClient(string(raw)), data); err != nil {
		t.Fatalf("an unrelated container refused the writer: %v", err)
	}
}

func TestInventoryEmptyVolumeSourceDoesNotWeakenBindOrOverlap(t *testing.T) {
	id := strings.Repeat("e", 64)
	for name, mounts := range map[string]string{
		"bind without source":       `[{"Type":"bind","Source":""}]`,
		"bind with absent source":   `[{"Type":"bind"}]`,
		"unknown type empty source": `[{"Type":"image","Source":""}]`,
		"relative volume source":    `[{"Type":"volume","Source":"rel"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readCandidateIPCInventory(context.Background(), inventoryClient(`[{"Id":"`+id+`","Mounts":`+mounts+`}]`))
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("want ErrInvalidResponse, got %v", err)
			}
		})
	}
	data := t.TempDir()
	body := `[{"Id":"` + id + `","Mounts":[{"Type":"volume","Source":""},{"Type":"bind","Source":"` + data + `"}]}]`
	if err := requireNoContainerMountReferences(context.Background(), inventoryClient(body), data); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("an overlapping mount beside an anonymous volume was admitted: %v", err)
	}
}
