//go:build linux

package driver

import (
	"context"
	"errors"
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

// A daemon on a unix socket whose first volume inspect answers "Connection: close" and, when asked, makes the
// daemon unacceptable before the next connection: the pass then opens a connection mid-way whose peer is not
// identified, after the first read has passed.
func midPassDaemon(t *testing.T, flipAfterFirstVolume bool) (client *http.Client, namespace string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "portal-midpass-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	root, _ := filepath.EvalSymlinks(dir)
	namespace = filepath.Join(root, "data", "ns-a")
	if err := os.MkdirAll(namespace, 0700); err != nil {
		t.Fatal(err)
	}
	var accept atomic.Bool
	accept.Store(true)
	acceptExecutable(t, func(string) bool { return accept.Load() })
	var mounts []string
	for _, name := range []string{"vol1", "vol2"} {
		mounts = append(mounts, fmt.Sprintf(`{"Type":"volume","Name":"%s","Driver":"local","Source":"%s/docker/%s/_data","Destination":"/%s"}`, name, root, name, name))
	}
	listing := `[{"Id":"` + strings.Repeat("a", 64) + `","Mounts":[` + strings.Join(mounts, ",") + `]}]`
	mux := http.NewServeMux()
	mux.HandleFunc("/v"+APIVersion+"/containers/json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(listing)) })
	mux.HandleFunc("/v"+APIVersion+"/volumes/vol1", func(w http.ResponseWriter, r *http.Request) {
		if flipAfterFirstVolume {
			accept.Store(false)
			w.Header().Set("Connection", "close") // the next request is a new connection
		}
		w.Write([]byte(`{"Driver":"local","Options":null}`))
	})
	mux.HandleFunc("/v"+APIVersion+"/volumes/vol2", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"Driver":"local","Options":null}`)) })
	socket := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &http.Client{Transport: allowListTransport{next: transport, peer: &daemonPeer{}}, Timeout: 5 * time.Second}, namespace
}

func TestPassRefusesWhenAConnectionOpenedMidPassIsNotIdentified(t *testing.T) {
	client, namespace := midPassDaemon(t, true)
	err := requireNoContainerMountReferences(context.Background(), client, namespace)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a pass whose later inspect ran on an unidentified connection returned %v", err)
	}
	// The same pass with an identified daemon passes.
	client, namespace = midPassDaemon(t, false)
	if err := requireNoContainerMountReferences(context.Background(), client, namespace); err != nil {
		t.Fatal(err)
	}
}
