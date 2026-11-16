//go:build linux

package driver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A host with 300 containers, each on its own local volume, against a real HTTP daemon on a unix socket (one
// connection per request, as the runner's client dials). The whole pass has a 5 s deadline.
func TestInventoryPassWithManyContainersAndVolumesFitsTheDeadline(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "portal-scale-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	root, _ := filepath.EvalSymlinks(dir)
	namespace := filepath.Join(root, "data", "ns-a")
	if err := os.MkdirAll(namespace, 0700); err != nil {
		t.Fatal(err)
	}
	const n = 300
	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf(`{"Id":"%064x","Mounts":[{"Type":"volume","Name":"vol%d","Driver":"local","Source":"%s/docker/vol%d/_data","Destination":"/v"},{"Type":"bind","Source":"%s/other","Destination":"/b"}]}`, i+1, i, root, i, root))
	}
	listing := "[" + strings.Join(parts, ",") + "]"
	var inspects atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v"+APIVersion+"/containers/json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(listing)) })
	mux.HandleFunc("/v"+APIVersion+"/volumes/", func(w http.ResponseWriter, r *http.Request) {
		inspects.Add(1)
		w.Write([]byte(`{"Driver":"local","Options":null}`))
	})
	socket := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	client := daemonClient(transport)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := requireNoContainerMountReferences(ctx, client, namespace); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("%d containers and %d volumes: %v, %d volume inspects", n, n, elapsed, inspects.Load())
	if inspects.Load() != n {
		t.Fatalf("%d inspects for %d volumes", inspects.Load(), n)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("the pass took %v of a 5 s deadline: reuse the connection", elapsed)
	}
}
