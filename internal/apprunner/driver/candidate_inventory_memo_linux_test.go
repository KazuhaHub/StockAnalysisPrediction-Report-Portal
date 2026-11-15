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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type inventoryMemoFixture struct {
	plan    containerPlan
	policy  containerCreate
	root    string
	target  string
	alias   string
	id      string
	listing any // what the daemon lists next; tests are sequential
	lists   atomic.Int64
}

func newInventoryMemoFixture(t *testing.T) *inventoryMemoFixture {
	t.Helper()
	raw, digest, reference := managedImageFixture(t)
	owner := containerFixture(t).owner
	owner.WriterFence = "1"
	root, err := os.MkdirTemp("/tmp", "portal-wm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	plan, err := planWritableCandidateContainer(raw, BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}, owner, containerRoots{Data: filepath.Join(root, "data"), Sockets: filepath.Join(root, "sockets")}, writableBindingFixture())
	if err != nil {
		t.Fatal(err)
	}
	f := &inventoryMemoFixture{plan: plan, root: root, id: strings.Repeat("a", 64)}
	json.Unmarshal(plan.body, &f.policy)
	for _, mount := range f.policy.HostConfig.Mounts {
		if err := os.MkdirAll(mount.Source, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.target = f.policy.HostConfig.Mounts[0].Source
	f.alias = filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Join(root, "elsewhere"), f.alias); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *inventoryMemoFixture) entry(id string, sources ...string) map[string]any {
	mounts := []map[string]any{}
	for _, s := range sources {
		mounts = append(mounts, map[string]any{"Type": "bind", "Source": s})
	}
	return map[string]any{"Id": id, "Mounts": mounts, "State": "running", "Status": "Up 3 minutes"}
}

func (f *inventoryMemoFixture) client() *http.Client {
	return daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		f.lists.Add(1)
		encoded, _ := json.Marshal(f.listing)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
	}))
}

func freshInventoryMemos(t *testing.T) {
	t.Helper()
	oldEntries, oldMounts := inventoryEntryMemo, mountPlanMemo
	inventoryEntryMemo = newDecisionMemo[candidateIPCInventoryEntry](64)
	mountPlanMemo = newDecisionMemo[candidateIPCMountPlanResult](64)
	t.Cleanup(func() { inventoryEntryMemo, mountPlanMemo = oldEntries, oldMounts })
}

func countResolutions(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	old := candidateIPCResolver
	candidateIPCResolver = func(p string) (string, error) { n.Add(1); return filepath.EvalSymlinks(p) }
	t.Cleanup(func() { candidateIPCResolver = old })
	return &n
}

// The policy has three mounts and a foreign container may reference many paths. Each distinct path of the
// inventory is resolved once per pass, not once per policy mount, and the pass still asks for one listing.
func TestInventoryPassResolvesEachSourceOnce(t *testing.T) {
	freshInventoryMemos(t)
	f := newInventoryMemoFixture(t)
	other := filepath.Join(f.root, "other-data")
	os.MkdirAll(other, 0700)
	f.listing = ([]map[string]any{
		f.entry(f.id, f.target),
		f.entry(strings.Repeat("c", 64), other, other, f.alias),
		f.entry(strings.Repeat("d", 64), other),
	})
	resolved := countResolutions(t)
	got, err := managedCandidateReference(context.Background(), f.client(), f.plan)
	if err != nil || got != f.id {
		t.Fatal("reference", got, err)
	}
	if n := resolved.Load(); n != 3 { // target, other, alias
		t.Fatalf("each distinct source must be resolved once per pass: %d resolutions", n)
	}
	if f.lists.Load() != 1 {
		t.Fatalf("a pass lists the daemon once, not per container: %d", f.lists.Load())
	}
}

// Entries that were accepted are not decoded again, and every refusal stays a refusal with the memory warm.
func TestInventoryEntryMemoServesAcceptedEntriesAndNeverRefusals(t *testing.T) {
	freshInventoryMemos(t)
	f := newInventoryMemoFixture(t)
	f.listing = ([]map[string]any{f.entry(f.id, f.target), f.entry(strings.Repeat("c", 64), filepath.Join(f.root, "x"))})
	for i := 0; i < 3; i++ {
		if got, err := managedCandidateReference(context.Background(), f.client(), f.plan); err != nil || got != f.id {
			t.Fatal("reference", i, got, err)
		}
	}
	if hits, misses, _ := inventoryEntryMemo.stats(); hits != 4 || misses != 2 {
		t.Fatalf("accepted entries must be decoded once: hits=%d misses=%d", hits, misses)
	}
	bad := map[string]func() any{
		"duplicate key":   func() any { return json.RawMessage(`[{"Id":"` + f.id + `","Id":"` + f.id + `","Mounts":[]}]`) },
		"case alias":      func() any { return json.RawMessage(`[{"Id":"` + f.id + `","mounts":[],"Mounts":[]}]`) },
		"malformed id":    func() any { return []map[string]any{f.entry("name-only", f.target)} },
		"relative source": func() any { return []map[string]any{f.entry(f.id, "relative/path")} },
		"unknown mount": func() any {
			return []map[string]any{{"Id": f.id, "Mounts": []map[string]any{{"Type": "weird", "Source": f.target}}}}
		},
		"duplicate entry": func() any { return []map[string]any{f.entry(f.id, f.target), f.entry(f.id, f.target)} },
		"mounts missing":  func() any { return []map[string]any{{"Id": f.id}} },
	}
	for name, build := range bad {
		t.Run(name, func(t *testing.T) {
			f.listing = (build())
			for i := 0; i < 2; i++ {
				if got, err := managedCandidateReference(context.Background(), f.client(), f.plan); err == nil {
					t.Fatal("a refused inventory was accepted with a warm memory", got)
				}
			}
		})
	}
}

// With the memory warm, every change the guarantee exists for is still caught on the very next pass:
// a foreign container that starts referencing the namespace, a symlink retargeted into it, a parent
// directory mounted, a replaced container and a vanished one.
func TestWarmInventoryStillCatchesNamespaceExposure(t *testing.T) {
	f := newInventoryMemoFixture(t)
	foreign := strings.Repeat("c", 64)
	warm := func(t *testing.T) {
		freshInventoryMemos(t)
		f.listing = ([]map[string]any{f.entry(f.id, f.target), f.entry(foreign, filepath.Join(f.root, "unrelated"))})
		if got, err := managedCandidateReference(context.Background(), f.client(), f.plan); err != nil || got != f.id {
			t.Fatal("warm-up", got, err)
		}
	}
	t.Run("foreign container references the namespace", func(t *testing.T) {
		warm(t)
		f.listing = ([]map[string]any{f.entry(f.id, f.target), f.entry(foreign, f.target)})
		if _, err := managedCandidateReference(context.Background(), f.client(), f.plan); !errors.Is(err, ErrContainerConflict) {
			t.Fatal("foreign reference accepted", err)
		}
	})
	t.Run("foreign mounts a parent", func(t *testing.T) {
		warm(t)
		f.listing = ([]map[string]any{f.entry(f.id, f.target), f.entry(foreign, filepath.Dir(f.target))})
		if _, err := managedCandidateReference(context.Background(), f.client(), f.plan); !errors.Is(err, ErrContainerConflict) {
			t.Fatal("parent mount accepted", err)
		}
	})
	t.Run("symlink retargeted into the namespace", func(t *testing.T) {
		os.Remove(f.alias)
		if err := os.Symlink(filepath.Join(f.root, "elsewhere"), f.alias); err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(f.root, "elsewhere"), 0700)
		freshInventoryMemos(t)
		f.listing = ([]map[string]any{f.entry(f.id, f.target), f.entry(foreign, f.alias)})
		if got, err := managedCandidateReference(context.Background(), f.client(), f.plan); err != nil || got != f.id {
			t.Fatal("warm-up", got, err)
		}
		os.Remove(f.alias)
		if err := os.Symlink(f.target, f.alias); err != nil {
			t.Fatal(err)
		}
		if _, err := managedCandidateReference(context.Background(), f.client(), f.plan); !errors.Is(err, ErrContainerConflict) {
			t.Fatal("retargeted symlink accepted", err)
		}
	})
	t.Run("container replaced or gone", func(t *testing.T) {
		warm(t)
		f.listing = ([]map[string]any{f.entry(strings.Repeat("e", 64), f.target)})
		if got, err := managedCandidateReference(context.Background(), f.client(), f.plan); err != nil || got == f.id {
			t.Fatal("a replaced container kept its old identity", got, err)
		}
		f.listing = ([]map[string]any{f.entry(foreign, filepath.Join(f.root, "unrelated"))})
		if _, err := managedCandidateReference(context.Background(), f.client(), f.plan); !errors.Is(err, ErrContainerUncertain) {
			t.Fatal("a vanished container still referenced", err)
		}
	})
	t.Run("resolver error is not an absence", func(t *testing.T) {
		warm(t)
		old := candidateIPCResolver
		candidateIPCResolver = func(string) (string, error) { return "", os.ErrPermission }
		defer func() { candidateIPCResolver = old }()
		if _, err := managedCandidateReference(context.Background(), f.client(), f.plan); !errors.Is(err, ErrUnavailable) {
			t.Fatal("an unreadable source passed", err)
		}
	})
}

// The memories hold parsed results only. Daemon bytes are cleared by the pass that read them, so nothing
// the memos keep may be a byte buffer or a raw message.
func TestInventoryMemosKeepNoDaemonBytes(t *testing.T) {
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		switch typ.Kind() {
		case reflect.Slice, reflect.Array:
			if typ.Elem().Kind() == reflect.Uint8 {
				t.Fatalf("%s keeps bytes (%s)", path, typ)
			}
			walk(typ.Elem(), path+"[]")
		case reflect.Pointer, reflect.Map:
			walk(typ.Elem(), path+"*")
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				walk(typ.Field(i).Type, path+"."+typ.Field(i).Name)
			}
		}
	}
	walk(reflect.TypeOf(candidateIPCInventoryEntry{}), "inventory entry")
	walk(reflect.TypeOf(candidateIPCMountPlanResult{}), "mount plan")
	// A remembered entry still answers the pass with the same sources as a cold one.
	freshInventoryMemos(t)
	f := newInventoryMemoFixture(t)
	f.listing = []map[string]any{f.entry(f.id, f.target), f.entry(strings.Repeat("c", 64), filepath.Join(f.root, "x"))}
	for i := 0; i < 2; i++ {
		got, err := readCandidateIPCInventory(context.Background(), f.client())
		if err != nil || len(got) != 2 {
			t.Fatal("inventory", i, err)
		}
		sources, err := candidateIPCContainerSources(context.Background(), f.client(), got[0])
		if err != nil || len(sources) != 1 || sources[0] != f.target {
			t.Fatal("sources of a remembered entry", i, sources, err)
		}
	}
}

// Callers read each returned entry's Mounts (the acceptance suites hand it to candidateIPCMountSources), so a
// remembered entry must still carry them, including an empty list, and an absent key stays refused.
func TestInventoryReturnsMountsOnEveryPassAndRefusesAnAbsentKey(t *testing.T) {
	freshInventoryMemos(t)
	f := newInventoryMemoFixture(t)
	empty := strings.Repeat("e", 64)
	f.listing = []map[string]any{f.entry(f.id, f.target), {"Id": empty, "Mounts": []any{}, "State": "exited"}}
	for pass := 0; pass < 3; pass++ { // the first is cold, the rest warm
		entries, err := readCandidateIPCInventory(context.Background(), f.client())
		if err != nil || len(entries) != 2 {
			t.Fatal("inventory", pass, err)
		}
		for _, e := range entries {
			sources, err := candidateIPCMountSources(e.Mounts)
			if err != nil {
				t.Fatal("a returned entry lost its Mounts", pass, e.ID, err)
			}
			if (e.ID == f.id) != (len(sources) == 1) {
				t.Fatal("sources", pass, e.ID, sources)
			}
		}
		if string(entries[1].Mounts) != "[]" {
			t.Fatalf("empty Mounts changed: %q", entries[1].Mounts)
		}
	}
	f.listing = []map[string]any{{"Id": empty, "State": "exited"}}
	for pass := 0; pass < 2; pass++ {
		if _, err := readCandidateIPCInventory(context.Background(), f.client()); err == nil {
			t.Fatal("an entry without Mounts was accepted", pass)
		}
	}
}
