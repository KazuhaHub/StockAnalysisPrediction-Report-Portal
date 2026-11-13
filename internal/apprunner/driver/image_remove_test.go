package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// removeDaemon is a small Docker image and container API for the removal path.
type removeDaemon struct {
	t          *testing.T
	store      ImageStore
	images     map[string]bool
	containers []map[string]string // Id, ImageID, State
	deleteCode int                 // 0 means 200
	keepOnOK   bool                // a delete that answers 200 and leaves the image
	deletes    []string
	requests   []string
	failList   bool
}

func (d *removeDaemon) client() *http.Client {
	return daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		path := r.URL.EscapedPath()
		d.requests = append(d.requests, r.Method+" "+path)
		reply := func(status int, body any) (*http.Response, error) {
			raw, _ := json.Marshal(body)
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}, nil
		}
		switch {
		case r.Method == "GET" && path == "/v1.47/info":
			return reply(200, infoFor(d.store))
		case r.Method == "GET" && path == "/v1.47/images/json":
			if d.failList {
				return reply(500, map[string]string{"message": "boom"})
			}
			var list []map[string]any
			for id := range d.images {
				list = append(list, map[string]any{"Id": id, "RepoTags": []string{}})
			}
			return reply(200, append([]map[string]any{}, list...))
		case r.Method == "GET" && path == "/v1.47/containers/json":
			filter, _ := url.QueryUnescape(r.URL.Query().Get("filters"))
			var spec map[string][]string
			_ = json.Unmarshal([]byte(filter), &spec)
			out := []map[string]any{}
			for _, c := range d.containers {
				if ancestors := spec["ancestor"]; len(ancestors) > 0 && ancestors[0] != c["ImageID"] {
					continue
				}
				out = append(out, map[string]any{"Id": c["Id"], "ImageID": c["ImageID"], "State": c["State"], "Names": []string{"/x"}, "Labels": map[string]string{}})
			}
			return reply(200, out)
		case r.Method == "DELETE" && strings.HasPrefix(path, "/v1.47/images/sha256:"):
			id := strings.TrimPrefix(path, "/v1.47/images/")
			d.deletes = append(d.deletes, id)
			if r.URL.Query().Get("force") != "false" {
				d.t.Fatalf("image delete without force=false: %q", r.URL.RawQuery)
			}
			if d.deleteCode != 0 {
				return reply(d.deleteCode, map[string]string{"message": "conflict: unable to delete, image is being used by a container"})
			}
			if !d.keepOnOK {
				delete(d.images, id)
			}
			return reply(200, []any{})
		}
		d.t.Fatalf("unexpected daemon request %s %s", r.Method, path)
		return nil, nil
	}))
}

func newRemoveDaemon(t *testing.T, store ImageStore, f loadFixture) *removeDaemon {
	return &removeDaemon{t: t, store: store, images: map[string]bool{
		"sha256:" + strings.Repeat("e", 64): true,
		ExpectedImageID(store, f.entry):     true,
	}}
}

func (f loadFixture) removeRequest() RemoveImageRequest {
	return RemoveImageRequest{Descriptor: f.raw, ReleaseSHA256: f.parsed.SHA256, Platform: f.entry.Platform}
}

func TestRemoveImageRemovesByIDAndProvesAbsence(t *testing.T) {
	for _, store := range []ImageStore{ImageStoreClassic, ImageStoreContainerd} {
		t.Run(string(store), func(t *testing.T) {
			f, d := newLoadFixture(t), (*removeDaemon)(nil)
			d = newRemoveDaemon(t, store, f)
			want := ExpectedImageID(store, f.entry)
			got, err := removeImageHTTP(context.Background(), d.client(), f.removeRequest(), "amd64")
			if err != nil || got.State != RemoveStateRemoved || got.ImageID != want || got.Store != store || got.ObservedMS == 0 {
				t.Fatalf("remove: %+v %v", got, err)
			}
			if len(d.deletes) != 1 || d.deletes[0] != want {
				t.Fatalf("deleted %v, want exactly %s", d.deletes, want)
			}
			if d.images[want] || !d.images["sha256:"+strings.Repeat("e", 64)] {
				t.Fatalf("the daemon's images after the removal: %v", d.images)
			}
		})
	}
}

func TestRemoveImageAlreadyAbsentDeletesNothing(t *testing.T) {
	f := newLoadFixture(t)
	d := newRemoveDaemon(t, ImageStoreClassic, f)
	delete(d.images, ExpectedImageID(ImageStoreClassic, f.entry))
	got, err := removeImageHTTP(context.Background(), d.client(), f.removeRequest(), "amd64")
	if err != nil || got.State != RemoveStateAbsent || len(d.deletes) != 0 {
		t.Fatalf("absent image: %+v %v deletes=%v", got, err, d.deletes)
	}
}

// A container of any state that uses the image refuses the removal, typed, before any delete is sent.
func TestRemoveImageRefusedWhileAnyContainerUsesIt(t *testing.T) {
	for _, state := range []string{"running", "exited", "created", "paused", "dead"} {
		t.Run(state, func(t *testing.T) {
			f := newLoadFixture(t)
			d := newRemoveDaemon(t, ImageStoreClassic, f)
			id := ExpectedImageID(ImageStoreClassic, f.entry)
			d.containers = []map[string]string{
				{"Id": strings.Repeat("1", 64), "ImageID": "sha256:" + strings.Repeat("e", 64), "State": "running"},
				{"Id": strings.Repeat("2", 64), "ImageID": id, "State": state},
			}
			got, err := removeImageHTTP(context.Background(), d.client(), f.removeRequest(), "amd64")
			if err != nil || got.State != RemoveStateInUse || got.ImageID != id {
				t.Fatalf("in-use image: %+v %v", got, err)
			}
			if len(d.deletes) != 0 || !d.images[id] {
				t.Fatalf("an image used by a %s container was deleted: %v", state, d.deletes)
			}
		})
	}
}

// Even if the daemon's own conflict check is what notices the container, the answer is the typed refusal and never
// an uncertain effect.
func TestRemoveImageDaemonConflictIsTheTypedRefusal(t *testing.T) {
	f := newLoadFixture(t)
	d := newRemoveDaemon(t, ImageStoreClassic, f)
	d.deleteCode = 409
	got, err := removeImageHTTP(context.Background(), d.client(), f.removeRequest(), "amd64")
	if err != nil || got.State != RemoveStateInUse {
		t.Fatalf("daemon 409: %+v %v", got, err)
	}
	if !d.images[ExpectedImageID(ImageStoreClassic, f.entry)] {
		t.Fatal("the image disappeared after a refused removal")
	}
}

func TestRemoveImageUncertaintyIsAnErrorNotAVerdict(t *testing.T) {
	f := newLoadFixture(t)
	t.Run("delete fails", func(t *testing.T) {
		d := newRemoveDaemon(t, ImageStoreClassic, f)
		d.deleteCode = 500
		got, err := removeImageHTTP(context.Background(), d.client(), f.removeRequest(), "amd64")
		if !errors.Is(err, ErrContainerUncertain) || got != (RemoveImageSnapshot{}) {
			t.Fatalf("delete 500: %+v %v", got, err)
		}
	})
	t.Run("image survives an acknowledged delete", func(t *testing.T) {
		d := newRemoveDaemon(t, ImageStoreClassic, f)
		d.keepOnOK = true
		got, err := removeImageHTTP(context.Background(), d.client(), f.removeRequest(), "amd64")
		if !errors.Is(err, ErrContainerUncertain) || got != (RemoveImageSnapshot{}) {
			t.Fatalf("absence not proven: %+v %v", got, err)
		}
	})
	t.Run("image list unreadable", func(t *testing.T) {
		d := newRemoveDaemon(t, ImageStoreClassic, f)
		d.failList = true
		if _, err := removeImageHTTP(context.Background(), d.client(), f.removeRequest(), "amd64"); err == nil || len(d.deletes) != 0 {
			t.Fatalf("an unreadable image list must not remove anything: %v %v", err, d.deletes)
		}
	})
}

func TestRemoveImageRefusesADescriptorThatDoesNotBind(t *testing.T) {
	f := newLoadFixture(t)
	d := newRemoveDaemon(t, ImageStoreClassic, f)
	request := f.removeRequest()
	request.ReleaseSHA256 = strings.Repeat("0", 64)
	if _, err := removeImageHTTP(context.Background(), d.client(), request, "amd64"); err == nil || len(d.deletes) != 0 {
		t.Fatalf("a mismatched release removed something: %v", err)
	}
	request = f.removeRequest()
	request.Platform = "linux/arm64"
	if _, err := removeImageHTTP(context.Background(), d.client(), request, "amd64"); err == nil || len(d.deletes) != 0 {
		t.Fatalf("a foreign platform removed something: %v", err)
	}
}

func TestImagePresentByIDTellsPresenceWithoutJudgingTheImage(t *testing.T) {
	f := newLoadFixture(t)
	d := newRemoveDaemon(t, ImageStoreClassic, f)
	want := ExpectedImageID(ImageStoreClassic, f.entry)
	got, err := imagePresentByIDHTTP(context.Background(), d.client(), f.raw, f.parsed.SHA256, f.entry.Platform, "amd64")
	if err != nil || got.State != RemoveStatePresent || got.ImageID != want || got.Store != ImageStoreClassic {
		t.Fatalf("present image: %+v %v", got, err)
	}
	delete(d.images, want)
	got, err = imagePresentByIDHTTP(context.Background(), d.client(), f.raw, f.parsed.SHA256, f.entry.Platform, "amd64")
	if err != nil || got.State != RemoveStateAbsent || got.ImageID != want {
		t.Fatalf("absent image: %+v %v", got, err)
	}
	for _, request := range d.requests {
		if strings.HasPrefix(request, "DELETE") {
			t.Fatalf("a presence check sent %s", request)
		}
	}
	d.failList = true
	if _, err := imagePresentByIDHTTP(context.Background(), d.client(), f.raw, f.parsed.SHA256, f.entry.Platform, "amd64"); err == nil {
		t.Fatal("an unreadable list answered a presence")
	}
}

func TestDockerAllowListStillOnlyDeletesByImageID(t *testing.T) {
	for _, path := range []string{"/v1.47/images/portal:latest", "/v1.47/images/sha256:" + strings.Repeat("a", 64) + "/json?x"} {
		if dockerRequestAllowed("DELETE", path) {
			t.Fatalf("delete of %s is allowed", path)
		}
	}
	if !dockerRequestAllowed("DELETE", "/v1.47/images/sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("delete by ID is no longer allowed")
	}
}
