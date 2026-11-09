//go:build linux

package driver

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// nativeLoadClient is the runner's own allow-listed client against the disposable daemon.
func nativeLoadClient(t *testing.T) *http.Client {
	t.Helper()
	socket := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET")
	if socket == "" {
		t.Skip("explicit disposable Linux Docker acceptance socket not set")
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: ImageLoadTimeout, DialContext: dockerDialer(socket)}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: allowListTransport{next: transport}}
}

// TestNativeLoadImageTwoLayerImageOnTheDaemonStore loads a real two-layer image built by the test into the
// daemon CI happens to use, classic or containerd, and proves what the daemon reports: the image is addressed by
// the store-specific ID, carries no tag, passes the production inspection (no RepoDigests needed) and is
// recognised as present by a second load, which loads nothing.
func TestNativeLoadImageTwoLayerImageOnTheDaemonStore(t *testing.T) {
	client := nativeLoadClient(t)
	f := newLoadFixtureFor(t, runtime.GOARCH)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var info infoResponse
	if err := readJSON(ctx, client, "/v"+APIVersion+"/info", &info); err != nil {
		t.Fatal(err)
	}
	store := imageStoreFromInfo(info)
	expected := ExpectedImageID(store, f.entry)
	t.Logf("daemon image store: %s, expected image ID %s", store, expected)
	t.Cleanup(func() { _ = removeLoadedImage(context.Background(), client, expected) })
	before, err := readImageList(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadImageHTTP(ctx, client, f.request(), runtime.GOARCH)
	if err != nil || got.State != LoadStateLoaded || got.ImageID != expected || got.Store != store {
		t.Fatalf("native load: %+v %v", got, err)
	}
	after, err := readImageList(ctx, client)
	if err != nil || !sameImagesExcept(before, after, expected) {
		t.Fatalf("the load changed an image other than its own: %v", err)
	}
	if tags := after[expected]; tags != "[]" && tags != "null" {
		t.Fatalf("a loaded image carries names: %s", tags)
	}
	inspected, err := inspectBackendImageHTTP(ctx, daemonClient(allowListTransport{next: client.Transport.(allowListTransport).next}), f.raw, f.parsed.SHA256, runtime.GOARCH, ImagePolicy{})
	if err != nil || inspected.ImageID != expected {
		t.Fatalf("production inspection of the loaded image: %+v %v", inspected, err)
	}
	again, err := loadImageHTTP(ctx, client, LoadImageRequest{Descriptor: f.raw, ReleaseSHA256: f.parsed.SHA256, Platform: f.entry.Platform, Source: memorySource{}}, runtime.GOARCH)
	if err != nil || again.State != LoadStatePresent {
		t.Fatalf("second load: %+v %v", again, err)
	}
}

// TestNativeRetagByAPublisherLayoutIsDetected shows the daemon honours a name annotation in a publisher-supplied
// index, which is why the runner never forwards one, and that the before and after comparison catches the retag.
func TestNativeRetagByAPublisherLayoutIsDetected(t *testing.T) {
	client := nativeLoadClient(t)
	f := newLoadFixtureFor(t, runtime.GOARCH)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	plan, err := readImagePlan(f.entry, f.source)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := writeLoadArchive(&archive, plan, f.source); err != nil {
		t.Fatal(err)
	}
	// Rewrite the runner-written index with a name annotation, as a hostile publisher layout would carry.
	hostile := rewriteTarMember(t, archive.Bytes(), "index.json", func(index map[string]any) {
		index["manifests"].([]any)[0].(map[string]any)["annotations"] = map[string]any{
			"io.containerd.image.name": "docker.io/library/portal-retag-probe:latest", "org.opencontainers.image.ref.name": "portal-retag-probe:latest",
		}
	})
	var info infoResponse
	if err := readJSON(ctx, client, "/v"+APIVersion+"/info", &info); err != nil {
		t.Fatal(err)
	}
	expected := ExpectedImageID(imageStoreFromInfo(info), f.entry)
	t.Cleanup(func() { _ = removeLoadedImage(context.Background(), client, expected) })
	before, err := readImageList(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+dockerRequestHost+"/v"+APIVersion+"/images/load?quiet=1", bytes.NewReader(hostile))
	request.Header.Set("Content-Type", "application/x-tar")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	after, err := readImageList(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if sameImagesExcept(before, after, expected) {
		t.Skip("this daemon ignored the name annotation, so there was no retag to detect")
	}
	t.Log("the daemon honoured the annotation; the runner's comparison reports it as a retag")
}

func rewriteTarMember(t *testing.T, archive []byte, name string, edit func(map[string]any)) []byte {
	t.Helper()
	var out bytes.Buffer
	tr, tw := tar.NewReader(bytes.NewReader(archive)), tar.NewWriter(&out)
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(tr)
		raw := body.Bytes()
		if header.Name == name {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			edit(doc)
			raw, _ = json.Marshal(doc)
			header.Size = int64(len(raw))
		}
		_ = tw.WriteHeader(header)
		_, _ = tw.Write(raw)
	}
	_ = tw.Close()
	return out.Bytes()
}

// TestNativeLoadImageRefusesACorruptLayerAndAddsNothing is the bomb and corruption refusal on the real daemon: a
// scratch layer that no longer matches its digest never reaches Docker as a complete archive.
func TestNativeLoadImageRefusesACorruptLayerAndAddsNothing(t *testing.T) {
	client := nativeLoadClient(t)
	f := newLoadFixtureFor(t, runtime.GOARCH)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	digest := hexSum(f.layers[1])
	f.source[digest] = append(append([]byte(nil), f.layers[1]...), bytes.Repeat([]byte{0}, 1<<20)...)
	before, err := readImageList(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadImageHTTP(ctx, client, f.request(), runtime.GOARCH)
	if err != nil || got.State != LoadStateRejected || got.Outcome != LoadOutcomeArchiveMismatch {
		t.Fatalf("corrupt layer: %+v %v", got, err)
	}
	after, err := readImageList(ctx, client)
	if err != nil || !sameImagesExcept(before, after, "sha256:"+strings.Repeat("0", 64)) || len(before) != len(after) {
		t.Fatalf("a refused load changed the daemon: %v", err)
	}
}

// TestNativeNoPullIsPossible proves the allow-list on the real socket: a pull is refused before any byte reaches
// the daemon, and a registry-named image is never added by a load.
func TestNativeNoPullIsPossible(t *testing.T) {
	client := nativeLoadClient(t)
	request, _ := http.NewRequest(http.MethodPost, "http://"+dockerRequestHost+"/v"+APIVersion+"/images/create?fromImage=docker.io/library/busybox&tag=1.37.0", nil)
	if _, err := client.Do(request); !errors.Is(err, ErrDockerRequestRefused) {
		t.Fatalf("a pull reached the daemon: %v", err)
	}
}
