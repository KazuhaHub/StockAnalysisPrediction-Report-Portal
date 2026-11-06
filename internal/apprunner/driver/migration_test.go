package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func migrationFixture(t *testing.T) (containerPlan, []byte, appplatform.DataMigration) {
	t.Helper()
	raw, _, _ := managedImageFixture(t)
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	data := d["data"].(map[string]any)
	data["schema"], data["readable_schemas"] = 2, []int{2}
	data["migrations"] = []any{map[string]any{"from": 1, "to": 2, "entrypoint": []string{"/app/migrate", "--apply"}, "wall_clock_seconds": 60}}
	migrated, _ := json.Marshal(d)
	parsed, err := appplatform.ParseDescriptor(migrated)
	if err != nil {
		t.Fatal(err)
	}
	step := parsed.Manifest.Data.Migrations[0]
	id := func(s string) string { return s + "0000000-0000-4000-8000-000000000000" }
	owner := containerOwner{DeploymentID: id("1"), ControllerEpoch: id("2"), InstallationID: id("3"), OperationID: id("4"), ResourceID: id("5"), NamespaceID: id("6"), ReleaseSHA256: parsed.SHA256, Generation: "2", ControlFence: "2", WriterFence: "6"}
	image := BackendImageInspection{ReleaseSHA256: parsed.SHA256, Reference: parsed.Manifest.Backend.Image, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
	binding := MigrationBinding{
		EffectID: id("7"), ClaimID: id("8"), PayloadSHA256: strings.Repeat("a", 64),
		OwnershipSHA256: strings.Repeat("b", 64), CheckpointReceiptSHA256: strings.Repeat("c", 64),
	}
	plan, err := planMigrationContainer(migrated, image, owner, containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps"}, binding, step)
	if err != nil {
		t.Fatal(err)
	}
	return plan, migrated, step
}

func TestPlanMigrationContainerNetworkNoneBackendBounds(t *testing.T) {
	plan, raw, step := migrationFixture(t)
	if !validContainerPlan(plan) {
		t.Fatal("migration plan invalid")
	}
	var request containerCreate
	if err := json.Unmarshal(plan.body, &request); err != nil {
		t.Fatal(err)
	}
	parsed, _ := appplatform.ParseDescriptor(raw)
	limits := parsed.Manifest.Backend.Resources
	h := request.HostConfig
	if !request.NetworkDisabled || h.NetworkMode != "none" || !h.ReadonlyRootfs || h.Privileged || h.AutoRemove ||
		strings.Join(h.CapDrop, ",") != "ALL" || h.RestartPolicy.Name != "no" ||
		h.Memory != int64(limits.MemoryMiB)<<20 || h.MemorySwap != h.Memory ||
		h.NanoCpus != int64(limits.CPUMillis)*1000000 || h.PidsLimit != int64(limits.PIDs) {
		t.Fatal("migration job escaped the backend isolation or bounds", h)
	}
	if strings.Join(h.SecurityOpt, ",") != "no-new-privileges:true,"+candidateSeccompOption {
		t.Fatal("migration job lacks the writable seccomp profile")
	}
	if len(h.Mounts) != 1 || h.Mounts[0].Target != "/data" || h.Mounts[0].ReadOnly || h.Mounts[0].Source != "/srv/portal-data/"+plan.owner.NamespaceID {
		t.Fatal("migration job must have exactly one read-write data mount", h.Mounts)
	}
	if len(request.Env) != 0 {
		t.Fatal("migration job inherited socket environment", request.Env)
	}
	if len(request.Entrypoint) != 1 || request.Entrypoint[0] != step.Entrypoint[0] || strings.Join(request.Cmd, " ") != "--apply" {
		t.Fatal("entrypoint not pinned", request.Entrypoint, request.Cmd)
	}
	if request.Labels[containerLabelPrefix+"format"] != "migration/1-dev.1" || request.Labels[containerLabelPrefix+"migration-effect"] == "" {
		t.Fatal("migration identity labels missing")
	}
	// The entrypoint is covered by the policy digest.
	other := request
	other.Cmd = []string{"--other"}
	delete(other.Labels, containerLabelPrefix+"policy-sha256")
	changed, _ := json.Marshal(other)
	if containerHash(changed) == request.Labels[containerLabelPrefix+"policy-sha256"] {
		t.Fatal("entrypoint is not pinned by the policy digest")
	}
}

func TestPlanMigrationRefusesSocketOrBrokerMount(t *testing.T) {
	plan, raw, step := migrationFixture(t)
	for name, mutate := range map[string]func(*containerCreate){
		"socket mount": func(r *containerCreate) {
			r.HostConfig.Mounts = append(r.HostConfig.Mounts, containerMount{Type: "bind", Source: "/run/portal-apps/x/2/http", Target: "/run/portal-app/http"})
		},
		"read-only data": func(r *containerCreate) { r.HostConfig.Mounts[0].ReadOnly = true },
		"environment": func(r *containerCreate) {
			r.Env = []string{"PORTAL_APP_BROKER_SOCKET=/run/portal-app/broker/broker.sock"}
		},
		"network":       func(r *containerCreate) { r.HostConfig.NetworkMode = "bridge" },
		"no entrypoint": func(r *containerCreate) { r.Entrypoint = nil },
	} {
		t.Run(name, func(t *testing.T) {
			var request containerCreate
			if err := json.Unmarshal(plan.body, &request); err != nil {
				t.Fatal(err)
			}
			mutate(&request)
			delete(request.Labels, containerLabelPrefix+"policy-sha256")
			policy, _ := json.Marshal(request)
			request.Labels[containerLabelPrefix+"policy-sha256"] = containerHash(policy)
			changed := plan
			changed.body, _ = json.Marshal(request)
			changed.bodySHA = containerHash(changed.body)
			if validContainerPlan(changed) {
				t.Fatal("unsafe migration plan accepted")
			}
		})
	}
	image := BackendImageInspection{ReleaseSHA256: plan.owner.ReleaseSHA256, Reference: "", ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
	parsed, _ := appplatform.ParseDescriptor(raw)
	image.Reference = parsed.Manifest.Backend.Image
	binding := MigrationBinding{EffectID: "70000000-0000-4000-8000-000000000000", ClaimID: "80000000-0000-4000-8000-000000000000", PayloadSHA256: strings.Repeat("a", 64), OwnershipSHA256: strings.Repeat("b", 64), CheckpointReceiptSHA256: strings.Repeat("c", 64)}
	roots := containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps"}
	foreign := step
	foreign.Entrypoint = []string{"/bin/sh", "-c", "id"}
	if _, err := planMigrationContainer(raw, image, plan.owner, roots, binding, foreign); err == nil {
		t.Fatal("entrypoint not declared by the descriptor accepted")
	}
	if _, err := planMigrationContainer(raw, image, plan.owner, roots, binding, step); err != nil {
		t.Fatal(err)
	}
	staticOwner := plan.owner
	staticOwner.ReleaseSHA256 = strings.Repeat("f", 64)
	if _, err := planMigrationContainer(raw, image, staticOwner, roots, binding, step); err == nil {
		t.Fatal("unbound release accepted")
	}
}

type fakeJobDaemon struct {
	t        *testing.T
	plan     containerPlan
	mu       sync.Mutex
	state    string
	exitCode int
	calls    []string
	exitSelf time.Duration
	started  time.Time
	killed   bool
	present  bool
	named    []string
}

func (d *fakeJobDaemon) round(r *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, r.Method+" "+r.URL.Path)
	reply := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	id := strings.Repeat("c", 64)
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1.47/containers/create":
		d.state, d.present = "created", true
		return reply(201, `{"Id":"`+id+`","Warnings":[]}`)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/start"):
		d.state, d.started = "running", time.Now()
		return reply(204, "")
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/kill"):
		if r.URL.Query().Get("signal") != "SIGKILL" {
			d.t.Fatal("kill must use SIGKILL")
		}
		d.state, d.exitCode, d.killed = "exited", 137, true
		return reply(204, "")
	case r.Method == "DELETE":
		if d.state != "exited" && d.state != "created" {
			return reply(409, `{}`)
		}
		d.present = false
		return reply(204, "")
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1.47/containers/json"):
		filters := r.URL.Query().Get("filters")
		if !strings.Contains(r.URL.RawQuery, "all=1") {
			d.t.Fatal("absence proof must include stopped containers")
		}
		d.named = append(d.named, filters)
		if d.present {
			return reply(200, `[{"Id":"`+id+`","Names":["/`+d.plan.name+`"],"Labels":{}}]`)
		}
		return reply(200, `[]`)
	case r.Method == "GET":
		if d.exitSelf > 0 && d.state == "running" && time.Since(d.started) > d.exitSelf {
			d.state = "exited"
		}
		v := containerInspectFixture(d.t, d.plan)
		state := v["State"].(map[string]any)
		state["Status"], state["Running"], state["ExitCode"] = d.state, d.state == "running", d.exitCode
		raw, _ := json.Marshal(v)
		return reply(200, string(raw))
	}
	d.t.Fatal("unexpected daemon request", r.Method, r.URL)
	return nil, nil
}

func TestMigrationJobKilledAtWallClock(t *testing.T) {
	plan, _, _ := migrationFixture(t)
	daemon := &fakeJobDaemon{t: t, plan: plan}
	client := daemonClient(transportFunc(daemon.round))
	result, err := runMigrationJobHTTP(context.Background(), client, plan, 80*time.Millisecond, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !daemon.killed || !result.Killed || result.ExitCode != 137 || result.NativeID != strings.Repeat("c", 64) {
		t.Fatalf("job outliving the wall clock was not killed: %+v", result)
	}
	// A job that exits on its own is not killed and reports its own exit code.
	daemon = &fakeJobDaemon{t: t, plan: plan, exitSelf: 20 * time.Millisecond}
	result, err = runMigrationJobHTTP(context.Background(), daemonClient(transportFunc(daemon.round)), plan, 5*time.Second, 5*time.Millisecond)
	if err != nil || daemon.killed || result.Killed || result.ExitCode != 0 {
		t.Fatalf("self-exiting job: %+v %v", result, err)
	}
	// A cancelled caller does not stop a claimed job: the helper keeps polling until a definite end.
	daemon = &fakeJobDaemon{t: t, plan: plan}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runMigrationJobHTTP(ctx, daemonClient(transportFunc(daemon.round)), plan, 50*time.Millisecond, 5*time.Millisecond); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled context did not fail before any native call", err)
	}
	if len(daemon.calls) != 0 {
		t.Fatal("native call after cancellation", daemon.calls)
	}
}

func TestMigrationAbsenceByNameAndLabel(t *testing.T) {
	plan, _, _ := migrationFixture(t)
	daemon := &fakeJobDaemon{t: t, plan: plan, present: true, state: "exited"}
	client := daemonClient(transportFunc(daemon.round))
	if err := proveMigrationAbsentHTTP(context.Background(), client, plan); !errors.Is(err, ErrContainerConflict) {
		t.Fatal("a present job was proven absent", err)
	}
	daemon.present = false
	daemon.named = nil
	if err := proveMigrationAbsentHTTP(context.Background(), client, plan); err != nil {
		t.Fatal(err)
	}
	if len(daemon.named) != 2 || !strings.Contains(daemon.named[0], `"name"`) || !strings.Contains(daemon.named[1], `"label"`) ||
		!strings.Contains(daemon.named[1], containerLabelPrefix+"resource="+plan.owner.ResourceID) {
		t.Fatal("absence must be proven by both name and label", daemon.named)
	}
	// Removal then absence: the removal request needs an exited job.
	daemon = &fakeJobDaemon{t: t, plan: plan, present: true, state: "exited"}
	client = daemonClient(transportFunc(daemon.round))
	if err := removeAndProveMigrationAbsentHTTP(context.Background(), client, plan, strings.Repeat("c", 64)); err != nil || daemon.present {
		t.Fatal("job not removed", err)
	}
}
