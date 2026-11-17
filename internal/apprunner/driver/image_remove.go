package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Removing a managed image the Portal says no installation references any more. The runner acts on the image ID a
// load of the same descriptor entry produced, and only when no container of any state uses it.

// Outcomes of a removal. In use is a refusal, not an effect: nothing was removed and nothing is uncertain.
const (
	RemoveStateRemoved = "removed"
	RemoveStateAbsent  = "absent"
	RemoveStateInUse   = "in_use"
	// RemoveStateConflict is the daemon's own 409 after the runner's guard found no container: the image is
	// referenced some other way (for example by several repositories). Like in use it is a refusal, and retryable.
	RemoveStateConflict = "conflict"
	// RemoveStatePresent is only ever an observation of an image that is in the daemon. It is never settled.
	RemoveStatePresent = "present"
)

// ErrImageInUse is the typed reason the daemon (or the runner's own check) refuses to remove an image that a
// container still uses. It is never an uncertain effect.
var ErrImageInUse = errors.New("a container uses the image (image_in_use)")

// RemoveImageRequest is trusted infrastructure input from a verified, durably claimed journal attempt.
type RemoveImageRequest struct {
	Descriptor    []byte
	ReleaseSHA256 string
	Platform      string
}

// RemoveImageSnapshot is the native observation of one removal. It names no tag, path or native handle.
type RemoveImageSnapshot struct {
	ReleaseSHA256 string
	Platform      string
	ImageID       string
	Store         ImageStore
	State         string
	ObservedMS    int64
}

type containerImageRef struct {
	ID      string `json:"Id"`
	ImageID string `json:"ImageID"`
}

// listContainersOfImage reads the containers of any state the daemon reports for a filter. A filter names the
// ancestor image; an empty filter lists every container.
func listContainersOfImage(ctx context.Context, client *http.Client, filter map[string][]string) ([]containerImageRef, error) {
	ctx, cancel := probeContext(ctx)
	defer cancel()
	query := url.Values{"all": []string{"1"}}
	if len(filter) != 0 {
		encoded, err := json.Marshal(filter)
		if err != nil {
			return nil, ErrInvalidResponse
		}
		query.Set("filters", string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dockerRequestHost+"/v"+APIVersion+"/containers/json?"+query.Encode(), nil)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.Join(ErrUnavailable, ctx.Err())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, errors.Join(ErrUnavailable, ctx.Err())
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil, ErrInvalidResponse
	}
	result := make([]containerImageRef, 0, len(entries))
	for _, entry := range entries {
		var listed containerImageRef
		if json.Unmarshal(entry, &listed) != nil || !containerSHA.MatchString(listed.ID) {
			return nil, ErrInvalidResponse
		}
		result = append(result, listed)
	}
	return result, nil
}

// imageInUse reports whether any container, running or not, was created from the image. Two independent questions
// are asked: the daemon's ancestor filter, and a scan of every container's own image ID. Either one answering yes
// refuses the removal, and a failure to ask is an error, never "not in use".
func imageInUse(ctx context.Context, client *http.Client, id string) (bool, error) {
	byAncestor, err := listContainersOfImage(ctx, client, map[string][]string{"ancestor": {id}})
	if err != nil {
		return false, err
	}
	if len(byAncestor) != 0 {
		return true, nil
	}
	all, err := listContainersOfImage(ctx, client, nil)
	if err != nil {
		return false, err
	}
	for _, c := range all {
		if c.ImageID == id {
			return true, nil
		}
	}
	return false, nil
}

// deleteImageByID removes the image with force=false, so the daemon enforces its own container check. A 409 is the
// typed refusal; a 404 means the image is already gone; anything else leaves the outcome uncertain.
func deleteImageByID(ctx context.Context, client *http.Client, id string) error {
	ctx, cancel := probeContext(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://"+dockerRequestHost+"/v"+APIVersion+"/images/"+id+"?force=false&noprune=false", nil)
	if err != nil || !nativeImageID.MatchString(id) {
		return ErrInvalidResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.Join(ErrContainerUncertain, ctx.Err())
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	switch response.StatusCode {
	case http.StatusOK, http.StatusNotFound:
		return nil
	case http.StatusConflict:
		return ErrImageInUse
	}
	return fmt.Errorf("image delete status %d: %w", response.StatusCode, ErrContainerUncertain)
}

// bindRemoveEntry parses the descriptor and finds the image entry of the platform, as the load does.
func bindRemoveEntry(descriptor []byte, releaseSHA256, platform, arch string) (appplatform.BackendImage, error) {
	var zero appplatform.BackendImage
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != releaseSHA256 || parsed.Manifest.Backend == nil || "linux/"+arch != platform {
		return zero, ErrInvalidResponse
	}
	entry, ok := backendImageEntry(parsed.Manifest.Backend, platform)
	if !ok {
		return zero, ErrUnsupported
	}
	return entry, nil
}

func imageStoreOf(ctx context.Context, client *http.Client) (ImageStore, error) {
	var info infoResponse
	infoCtx, cancel := probeContext(ctx)
	err := readJSON(infoCtx, client, "/v"+APIVersion+"/info", &info)
	cancel()
	if err != nil {
		return "", err
	}
	return imageStoreFromInfo(info), nil
}

// removeImageHTTP removes one managed image by ID and proves it gone. The caller holds the runner's load lock and
// the durable unknown claim. An error means the outcome is uncertain: the same claim is completed by running it
// again, and the first thing that run does is look by ID, so an image that is already gone settles as absent.
func removeImageHTTP(ctx context.Context, client *http.Client, request RemoveImageRequest, arch string) (RemoveImageSnapshot, error) {
	var zero RemoveImageSnapshot
	entry, err := bindRemoveEntry(request.Descriptor, request.ReleaseSHA256, request.Platform, arch)
	if err != nil {
		return zero, err
	}
	store, err := imageStoreOf(ctx, client)
	if err != nil {
		return zero, err
	}
	expected := ExpectedImageID(store, entry)
	snapshot := RemoveImageSnapshot{ReleaseSHA256: request.ReleaseSHA256, Platform: request.Platform, ImageID: expected, Store: store}
	before, err := readImageList(ctx, client)
	if err != nil {
		return zero, err
	}
	if _, present := before[expected]; !present {
		snapshot.State, snapshot.ObservedMS = RemoveStateAbsent, time.Now().UnixMilli()
		return snapshot, nil
	}
	used, err := imageInUse(ctx, client, expected)
	if err != nil {
		return zero, err
	}
	if used {
		snapshot.State, snapshot.ObservedMS = RemoveStateInUse, time.Now().UnixMilli()
		return snapshot, nil
	}
	if err := deleteImageByID(ctx, client, expected); err != nil {
		if errors.Is(err, ErrImageInUse) {
			snapshot.State, snapshot.ObservedMS = RemoveStateConflict, time.Now().UnixMilli()
			return snapshot, nil
		}
		return zero, err
	}
	after, err := readImageList(ctx, client)
	if err != nil {
		return zero, err
	}
	if _, present := after[expected]; present {
		return zero, fmt.Errorf("image still listed after its removal: %w", ErrContainerUncertain)
	}
	snapshot.State, snapshot.ObservedMS = RemoveStateRemoved, time.Now().UnixMilli()
	return snapshot, nil
}

// imagePresentByIDHTTP is the read-only question that resolves an unknown removal: is the image the descriptor
// entry implies in the daemon by ID? It judges nothing about the image and changes nothing. The snapshot carries the
// ID and the store so an absent image can be settled without asking the daemon to remove anything.
func imagePresentByIDHTTP(ctx context.Context, client *http.Client, descriptor []byte, releaseSHA256, platform, arch string) (RemoveImageSnapshot, error) {
	var zero RemoveImageSnapshot
	entry, err := bindRemoveEntry(descriptor, releaseSHA256, platform, arch)
	if err != nil {
		return zero, err
	}
	store, err := imageStoreOf(ctx, client)
	if err != nil {
		return zero, err
	}
	listed, err := readImageList(ctx, client)
	if err != nil {
		return zero, err
	}
	expected := ExpectedImageID(store, entry)
	snapshot := RemoveImageSnapshot{ReleaseSHA256: releaseSHA256, Platform: platform, ImageID: expected, Store: store, State: RemoveStateAbsent, ObservedMS: time.Now().UnixMilli()}
	if _, present := listed[expected]; present {
		snapshot.State = RemoveStatePresent
	}
	return snapshot, nil
}
