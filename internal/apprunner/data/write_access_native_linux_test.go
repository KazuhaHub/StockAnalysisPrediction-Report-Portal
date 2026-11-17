//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeDataWriteAccess(t *testing.T) {
	path := os.Getenv("APP_DATA_NATIVE_ROOT")
	if path == "" {
		t.Skip("explicit disposable XFS write-access fixture only")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 || !filepath.IsAbs(path) || os.Getenv("APP_DATA_NATIVE_PHASE") != "" {
		t.Fatal("write-access fixture requires the initial disposable root phase")
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	root, err := Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	inode, err := strconv.ParseUint(os.Getenv("APP_DATA_NATIVE_INODE"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	pin := RootIdentity{FilesystemUUID: os.Getenv("APP_DATA_NATIVE_UUID"), Inode: inode}
	ctx := context.Background()
	allocation, err := root.Prepare(ctx, pin, Request{nativeNamespace(70), 4970, 8 << 20, 128})
	if err != nil {
		t.Fatal(err)
	}
	assertSealed := func() {
		t.Helper()
		got, err := root.Inspect(ctx, pin, allocation.NamespaceID, allocation.ProjectID)
		if err != nil || !sameNamespaceAllocation(got, allocation) {
			t.Fatal("refusal changed sealed allocation", err)
		}
	}
	if _, err := root.InspectWriteAccess(ctx, pin, allocation.NamespaceID, allocation.ProjectID); err == nil {
		t.Fatal("sealed namespace reported as write access")
	}
	for _, change := range []func(*Observation){
		func(o *Observation) { o.NamespaceID = nativeNamespace(71) },
		func(o *Observation) { o.ProjectID++ },
		func(o *Observation) { o.Device++ },
		func(o *Observation) { o.Inode++ },
		func(o *Observation) { o.ByteLimit += 512 },
		func(o *Observation) { o.InodeLimit++ },
		func(o *Observation) { o.Inherited = false },
	} {
		wrong := allocation
		change(&wrong)
		if got, err := root.EnableWriteAccess(ctx, pin, wrong); err == nil || got != (WriteAccessObservation{}) {
			t.Fatal("changed allocation enabled write access")
		}
		assertSealed()
	}
	wrongRoot := pin
	wrongRoot.Inode++
	if _, err := root.EnableWriteAccess(ctx, wrongRoot, allocation); err == nil {
		t.Fatal("wrong root enabled write access")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := root.EnableWriteAccess(canceled, pin, allocation); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled access mutation", err)
	}
	assertSealed()
	// The private provider serializes a physical one-way transition. This is not
	// proof of controller fencing or exclusion of mounted/running app writers.
	results := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() { <-start; _, err := root.EnableWriteAccess(ctx, pin, allocation); results <- err }()
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("physical transition repeated or never completed", successes)
	}
	got, err := root.InspectWriteAccess(ctx, pin, allocation.NamespaceID, allocation.ProjectID)
	if err != nil || !sameNamespaceAllocation(got.Namespace, allocation) {
		t.Fatal("write-access observation", err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(input)
	if err != nil {
		t.Fatal("reopen fixed physical profile", err)
	}
	defer reopened.Close()
	root = reopened
	got, err = root.InspectWriteAccess(ctx, pin, allocation.NamespaceID, allocation.ProjectID)
	if err != nil || !sameNamespaceAllocation(got.Namespace, allocation) {
		t.Fatal("write-access profile lost on provider reopen", err)
	}
	if _, err := root.Inspect(ctx, pin, allocation.NamespaceID, allocation.ProjectID); err == nil {
		t.Fatal("writable namespace adopted as sealed")
	}
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(path, allocation.NamespaceID), &st); err != nil || st.Uid != 0 || st.Gid != writeAccessGID || st.Mode&07777 != writeAccessMode {
		t.Fatal("root ownership/setgid profile lost", err)
	}
	// A partial previous transition and arbitrary ownership/mode drift are never
	// repaired by either mutation or observation.
	partial, err := root.Prepare(ctx, pin, Request{nativeNamespace(71), 4971, 8 << 20, 128})
	if err != nil {
		t.Fatal(err)
	}
	partialPath := filepath.Join(path, partial.NamespaceID)
	if err := os.Chown(partialPath, 0, writeAccessGID); err != nil {
		t.Fatal(err)
	}
	if _, err := root.EnableWriteAccess(ctx, pin, partial); err == nil {
		t.Fatal("partially changed namespace repaired")
	}
	if _, err := root.InspectWriteAccess(ctx, pin, partial.NamespaceID, partial.ProjectID); err == nil {
		t.Fatal("partial namespace adopted")
	}
	if err := unix.Stat(partialPath, &st); err != nil || st.Gid != writeAccessGID || st.Mode&07777 != 0700 {
		t.Fatal("partial state changed on refusal", err)
	}
	if err := os.Chmod(filepath.Join(path, allocation.NamespaceID), 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := root.InspectWriteAccess(ctx, pin, allocation.NamespaceID, allocation.ProjectID); err == nil {
		t.Fatal("write-access permission drift accepted")
	}
	t.Log("NATIVE_DATA_WRITE_ACCESS_TRANSITION_VERIFIED")
}
