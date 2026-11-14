//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
		"volume without source key": `[{"Type":"volume","Driver":"local"}]`,
		"volume without any source": `[{"Type":"volume"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readCandidateIPCInventory(context.Background(), inventoryClient(`[{"Id":"`+id+`","Mounts":`+mounts+`}]`))
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("want ErrInvalidResponse, got %v", err)
			}
		})
	}
	data := t.TempDir()
	body := `[{"Id":"` + id + `","Mounts":[{"Type":"volume","Driver":"local","Source":""},{"Type":"bind","Source":"` + data + `"}]}]`
	if err := requireNoContainerMountReferences(context.Background(), inventoryClient(body), data); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("an overlapping mount beside an anonymous volume was admitted: %v", err)
	}
}

// routedClient serves the listing at /containers/json and an inspect body per
// container id, and records the paths it was asked for.
func routedClient(listing string, inspect map[string]string, asked *[]string) *http.Client {
	return daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		*asked = append(*asked, r.URL.Path)
		body, status := listing, 200
		if r.URL.Path != "/v"+APIVersion+"/containers/json" {
			var ok bool
			if body, ok = inspect[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v"+APIVersion+"/containers/"), "/json")]; !ok {
				body, status = "{}", 404
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
}

func pluginVolumeFixture(driver string) (id, listing string) {
	id = strings.Repeat("f", 64)
	d := ""
	if driver != "-" {
		d = `,"Driver":"` + driver + `"`
	}
	return id, `[{"Id":"` + id + `","Mounts":[{"Type":"volume","Name":"v","Source":""` + d + `,"Destination":"/db"}]}]`
}

func TestPluginVolumeWithoutSourceIsResolvedByInspect(t *testing.T) {
	data := t.TempDir()
	for name, driver := range map[string]string{"plugin": "rexray", "empty": "", "absent": "-"} {
		t.Run(name+" overlapping", func(t *testing.T) {
			id, listing := pluginVolumeFixture(driver)
			var asked []string
			client := routedClient(listing, map[string]string{id: `{"Id":"` + id + `","Mounts":[{"Type":"volume","Name":"v","Source":"` + data + `/child","Destination":"/db"}]}`}, &asked)
			if err := requireNoContainerMountReferences(context.Background(), client, data); !errors.Is(err, ErrContainerConflict) {
				t.Fatalf("a volume resolving under the data root was admitted: %v", err)
			}
			if len(asked) != 2 {
				t.Fatalf("requests %v", asked)
			}
		})
		t.Run(name+" elsewhere", func(t *testing.T) {
			id, listing := pluginVolumeFixture(driver)
			var asked []string
			client := routedClient(listing, map[string]string{id: `{"Mounts":[{"Type":"volume","Source":"/mnt/elsewhere","Destination":"/db"}]}`}, &asked)
			if err := requireNoContainerMountReferences(context.Background(), client, data); err != nil {
				t.Fatalf("an unrelated resolved volume refused the writer: %v", err)
			}
		})
	}
}

func TestPluginVolumeThatStaysUnresolvedIsRefused(t *testing.T) {
	data := t.TempDir()
	for name, inspect := range map[string]string{
		"empty":       `{"Mounts":[{"Type":"volume","Source":"","Destination":"/db"}]}`,
		"relative":    `{"Mounts":[{"Type":"volume","Source":"rel","Destination":"/db"}]}`,
		"absent":      `{"Mounts":[{"Type":"volume","Destination":"/db"}]}`,
		"not matched": `{"Mounts":[{"Type":"volume","Source":"/x","Destination":"/other"}]}`,
		"no mounts":   `{"Mounts":[]}`,
		"duplicate":   `{"Mounts":[{"Type":"volume","Source":"/x","Destination":"/db"},{"Type":"volume","Source":"/y","Destination":"/db"}]}`,
		"not array":   `{"Mounts":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			id, listing := pluginVolumeFixture("rexray")
			var asked []string
			err := requireNoContainerMountReferences(context.Background(), routedClient(listing, map[string]string{id: inspect}, &asked), data)
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("want ErrInvalidResponse, got %v", err)
			}
		})
	}
	_, listing := pluginVolumeFixture("rexray")
	var asked []string
	if err := requireNoContainerMountReferences(context.Background(), routedClient(listing, map[string]string{}, &asked), data); err == nil {
		t.Fatal("a failed inspect admitted the volume")
	}
}

func TestLocalAnonymousVolumeIsSkippedWithoutInspect(t *testing.T) {
	id := strings.Repeat("a", 64)
	listing := `[{"Id":"` + id + `","Mounts":[{"Type":"volume","Name":"v","Source":"","Driver":"local","Destination":"/db"}]}]`
	var asked []string
	if err := requireNoContainerMountReferences(context.Background(), routedClient(listing, nil, &asked), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 {
		t.Fatalf("a local anonymous volume caused an inspect: %v", asked)
	}
}

func TestVolumeWithAbsoluteSourceOverlappingTheDataRootConflicts(t *testing.T) {
	data := t.TempDir()
	for name, source := range map[string]string{"equal": data, "ancestor": filepath.Dir(data), "child": filepath.Join(data, "child")} {
		t.Run(name, func(t *testing.T) {
			listing := `[{"Id":"` + strings.Repeat("b", 64) + `","Mounts":[{"Type":"volume","Driver":"local","Source":"` + source + `","Destination":"/db"}]}]`
			var asked []string
			if err := requireNoContainerMountReferences(context.Background(), routedClient(listing, nil, &asked), data); !errors.Is(err, ErrContainerConflict) {
				t.Fatalf("overlap through a volume admitted: %v", err)
			}
		})
	}
}
