package driver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/cpuset"
)

// AppCPUsUnavailableReason names a create refused because a CPU of the app set is no longer online.
const AppCPUsUnavailableReason = "app_cpus_unavailable"

// ErrAppCPUsUnavailable is Docker's "Requested CPUs are not available" on create: the host's online CPUs
// changed after the runner derived its app set. Readiness only reads the online list at start, so the runner
// must be restarted to derive the set again.
var ErrAppCPUsUnavailable = errors.New(AppCPUsUnavailableReason)

var (
	ErrContainerConflict  = errors.New("managed container identity conflict")
	ErrContainerUncertain = errors.New("managed container effect requires reconciliation")
	containerUUID         = regexp.MustCompile(`^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$`)
	containerSHA          = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

const containerLabelPrefix = "org.portal.app."
const candidateIPCProfile = "tmpfs-16m-1024/v1-dev"

type containerOwner struct {
	DeploymentID, ControllerEpoch, InstallationID, OperationID, ResourceID, NamespaceID string
	ReleaseSHA256, Generation, ControlFence, WriterFence                                string
}

// containerRoots are the infrastructure values every plan is built from. AppCPUs is the CPU list every app
// container is pinned to, or empty when the runner reserves no CPU; it reaches each create path because every
// path takes its roots from the runtime.
type containerRoots struct{ Data, Sockets, AppCPUs string }

// These package-private bytes are produced only by the closed policy builder.
// They are not signed execution authority. No production caller dispatches them
// until managed-command admission, native path pins and quota proofs are wired.
type containerPlan struct {
	name        string
	body        []byte
	bodySHA     string
	owner       containerOwner
	writable    *WritableCandidateBinding
	updateStart *UpdateStartBinding
	migration   *MigrationBinding
	profile     string
	// cpuset is the app CPU list the plan was built for. The body must pin exactly this set (or none, when it is
	// empty), so a body edited after planning, digest recomputed, is not the plan.
	cpuset string
}
type containerMount struct {
	Type        string
	Source      string
	Target      string
	ReadOnly    bool
	BindOptions struct {
		Propagation      string
		CreateMountpoint bool
	}
}

// appContainerCPUShares is the Docker CpuShares of every container the runner creates for app code. It is
// scheduling priority, not a cap: NanoCpus stays the hard limit, so an app on an idle host still gets its
// full quota. On cgroup v2 runc converts shares to cpu.weight, 256 becoming about 10 under the classic
// linear formula (possibly about 35 under the newer runc mapping), against 100 for a container with no
// shares set. The weight only divides CPU among sibling cgroups under the same parent. It helps the Portal
// and the runner when they sit beside the app containers, as the Portal, PostgreSQL and the runner do in
// system.slice in the production compose layout; it promises nothing in a layout where they are not
// siblings. Every create path (candidate, writable candidate, update start, migration job) derives from
// planCandidateContainer, and validContainerPlan refuses any other value, so the weight is part of the
// policy digest of each of them.
//
// CpusetCpus is the other CPU policy, and the stronger one: the CPUs app containers may run on at all. The
// runner reserves the lowest-numbered CPUs for the Portal, PostgreSQL and itself and pins every app container
// to the rest, because many quota-limited threads released together each period queue the Portal behind them no
// matter what weight it has. It is empty when nothing is reserved, and then it is not on the wire. A non-empty
// value joins the policy digest, so a runner that reserves CPUs computes different digests, and so builds
// different plans, than one that does not. A container created under one setting is a conflict under another,
// reported as ErrContainerCPUSetChanged when the cpuset is its only difference, and is never adopted, started or
// removed by the runner; the operator recovery is in doc 171.
const appContainerCPUShares = 256

type containerHostConfig struct {
	NetworkMode                                         string
	ReadonlyRootfs, Privileged, AutoRemove              bool
	CapDrop, SecurityOpt                                []string
	Memory, MemorySwap, NanoCpus, PidsLimit, ShmSize    int64
	CpuShares                                           int64
	CpusetCpus                                          string `json:"CpusetCpus,omitempty"`
	CgroupnsMode, IpcMode, PidMode, UTSMode, UsernsMode string
	Init                                                bool
	Mounts                                              []containerMount
	Tmpfs                                               map[string]string
	RestartPolicy                                       struct {
		Name              string
		MaximumRetryCount int
	}
	LogConfig struct {
		Type   string
		Config map[string]string
	}
	Ulimits []containerUlimit
}
type containerUlimit struct {
	Name       string
	Soft, Hard int64
}
type containerCreate struct {
	Image, User, Hostname, WorkingDir                                                   string
	NetworkDisabled, AttachStdin, AttachStdout, AttachStderr, OpenStdin, StdinOnce, Tty bool
	Env                                                                                 []string
	Entrypoint                                                                          []string `json:",omitempty"`
	Cmd                                                                                 []string `json:",omitempty"`
	Labels                                                                              map[string]string
	Healthcheck                                                                         struct{ Test []string }
	StopSignal                                                                          string
	StopTimeout                                                                         int
	HostConfig                                                                          containerHostConfig
}

func positiveContainerCounter(value string) bool {
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == value
}
func validContainerOwner(o containerOwner) bool {
	for _, id := range []string{o.DeploymentID, o.ControllerEpoch, o.InstallationID, o.OperationID, o.ResourceID, o.NamespaceID} {
		if !containerUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	return containerSHA.MatchString(o.ReleaseSHA256) && positiveContainerCounter(o.Generation) && positiveContainerCounter(o.ControlFence) && positiveContainerCounter(o.WriterFence)
}
func validContainerRoot(value string) bool {
	return len(value) <= 512 && len(value) > 1 && path.IsAbs(value) && path.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n\\")
}
func containerHash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
func ownerContainerLabels(o containerOwner) map[string]string {
	return map[string]string{
		containerLabelPrefix + "format": "candidate/1-dev.1", containerLabelPrefix + "deployment": o.DeploymentID, containerLabelPrefix + "epoch": o.ControllerEpoch,
		containerLabelPrefix + "installation": o.InstallationID, containerLabelPrefix + "operation": o.OperationID, containerLabelPrefix + "resource": o.ResourceID,
		containerLabelPrefix + "namespace": o.NamespaceID, containerLabelPrefix + "release": o.ReleaseSHA256, containerLabelPrefix + "generation": o.Generation,
		containerLabelPrefix + "control-fence": o.ControlFence, containerLabelPrefix + "writer-fence": o.WriterFence,
		containerLabelPrefix + "ipc-profile":     candidateIPCProfile,
		containerLabelPrefix + "seccomp-profile": candidateSeccompProfile,
	}
}

// planMemo remembers plans that passed validation, with their parsed policy. See decisionMemo.
var planMemo = newDecisionMemo[containerCreate](256)

// planKey digests every field of a plan, so a plan that differs in anything validation reads is another
// question. TestPlanMemoKeyCoversEveryPlanField fails when containerPlan grows a field this does not cover.
// The binding structs hold strings only, so %+v prints all of them.
func planKey(p containerPlan) memoKey {
	return memoKeyOf([]byte(p.name), p.body, []byte(p.bodySHA), []byte(fmt.Sprintf("%+v", p.owner)), bindingPart(p.writable), bindingPart(p.updateStart), bindingPart(p.migration), []byte(p.profile), []byte(p.cpuset))
}

func bindingPart[T any](binding *T) []byte {
	if binding == nil {
		return []byte("-")
	}
	return []byte(fmt.Sprintf("+%+v", *binding))
}

// validatedContainerBody is validContainerPlan that also returns the plan's parsed create policy. The result
// is shared between callers and must be treated as read-only.
func validatedContainerBody(plan containerPlan) (containerCreate, bool) {
	key := planKey(plan)
	if body, ok := planMemo.get(key); ok {
		return body, true
	}
	if !validContainerPlanUncached(plan) {
		return containerCreate{}, false
	}
	var body containerCreate
	if json.Unmarshal(plan.body, &body) != nil {
		return containerCreate{}, false
	}
	planMemo.put(key, body)
	return body, true
}

func validContainerPlan(plan containerPlan) bool {
	_, ok := validatedContainerBody(plan)
	return ok
}

func validContainerPlanUncached(plan containerPlan) bool {
	if !validContainerOwner(plan.owner) || plan.name != "portal-app-"+plan.owner.ResourceID || len(plan.body) == 0 || len(plan.body) > 32768 || plan.bodySHA != containerHash(plan.body) {
		return false
	}
	var request containerCreate
	if json.Unmarshal(plan.body, &request) != nil || request.HostConfig.CpuShares != appContainerCPUShares ||
		request.HostConfig.CpusetCpus != plan.cpuset || (plan.cpuset != "" && !cpuset.Valid(plan.cpuset)) {
		return false
	}
	labels := ownerContainerLabels(plan.owner)
	if plan.migration == nil && (len(request.Entrypoint) > 0 || len(request.Cmd) > 0) {
		return false
	}
	if plan.migration != nil {
		if plan.writable != nil || plan.updateStart != nil || !validMigrationPlan(plan, request) {
			return false
		}
		labels = migrationLabels(plan.owner, *plan.migration)
	}
	if plan.writable != nil {
		if !validWritableCandidateBinding(*plan.writable) || plan.owner.Generation != "1" || plan.owner.WriterFence != "1" || len(request.HostConfig.Mounts) != 3 || request.HostConfig.Mounts[0].Target != "/data" || request.HostConfig.Mounts[0].ReadOnly {
			return false
		}
		labels = writableCandidateLabels(plan.owner, *plan.writable)
	}
	if plan.updateStart != nil {
		profile := request.Labels[containerLabelPrefix+"sandbox-profile"]
		if plan.writable != nil || !validUpdateStartBinding(*plan.updateStart) ||
			!positiveContainerCounter(plan.owner.Generation) || plan.owner.Generation == "1" ||
			!positiveContainerCounter(plan.owner.WriterFence) || plan.owner.WriterFence == "1" ||
			(profile != UpdateStartReadOnlySandboxProfile && profile != UpdateStartWritableSandboxProfile) ||
			profile != plan.profile ||
			len(request.HostConfig.Mounts) != 3 || request.HostConfig.Mounts[0].Target != "/data" ||
			request.HostConfig.Mounts[0].ReadOnly != (profile == UpdateStartReadOnlySandboxProfile) {
			return false
		}
		labels = updateStartLabels(plan.owner, *plan.updateStart, profile)
	}
	for name, value := range labels {
		if request.Labels[name] != value {
			return false
		}
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, plan.body) {
		return false
	}
	policySHA := request.Labels[containerLabelPrefix+"policy-sha256"]
	delete(request.Labels, containerLabelPrefix+"policy-sha256")
	policy, err := json.Marshal(request)
	return err == nil && policySHA == containerHash(policy)
}

// planCandidateContainer fixes the Docker create policy from a parsed descriptor
// and inspected native image. All mount sources derive from infrastructure roots
// and platform IDs, never manifest paths. Candidate data is read-only; only its
// private HTTP socket directory is writable. This plan grants no writer lease.
func planCandidateContainer(raw []byte, image BackendImageInspection, owner containerOwner, roots containerRoots) (containerPlan, error) {
	empty := containerPlan{}
	parsed, err := appplatform.ParseDescriptor(raw)
	if candidateSeccompError != nil || err != nil || parsed.Manifest.Backend == nil || !validContainerOwner(owner) || parsed.SHA256 != owner.ReleaseSHA256 || image.ReleaseSHA256 != owner.ReleaseSHA256 || !nativeImageID.MatchString(image.ImageID) {
		return empty, ErrInvalidResponse
	}
	if reference, ok := backendImageReference(parsed.Manifest.Backend, image.Platform); !ok || image.Reference != reference {
		return empty, ErrInvalidResponse
	}
	declared := false
	for _, platform := range parsed.Manifest.Backend.Platforms {
		if platform == image.Platform && (platform == "linux/amd64" || platform == "linux/arm64") {
			declared = true
		}
	}
	if roots.AppCPUs != "" && !cpuset.Valid(roots.AppCPUs) {
		return empty, ErrInvalidResponse
	}
	if !declared || !validContainerRoot(roots.Data) || !validContainerRoot(roots.Sockets) || roots.Data == roots.Sockets || strings.HasPrefix(roots.Data, roots.Sockets+"/") || strings.HasPrefix(roots.Sockets, roots.Data+"/") {
		return empty, ErrInvalidResponse
	}
	sockets := path.Join(roots.Sockets, owner.InstallationID, owner.Generation)
	httpDir, brokerDir := path.Join(sockets, "http"), path.Join(sockets, "broker")
	if len(path.Join(httpDir, "app.sock")) > 107 || len(path.Join(brokerDir, "broker.sock")) > 107 {
		return empty, ErrInvalidResponse
	}
	limits := parsed.Manifest.Backend.Resources
	request := containerCreate{Image: image.ImageID, User: "65532:65532", Hostname: "app", WorkingDir: "/", NetworkDisabled: true,
		Env: []string{"PORTAL_APP_HTTP_SOCKET=/run/portal-app/http/app.sock", "PORTAL_APP_BROKER_SOCKET=/run/portal-app/broker/broker.sock"}, Labels: ownerContainerLabels(owner), StopSignal: "SIGTERM", StopTimeout: 10}
	request.Healthcheck.Test = []string{"NONE"}
	h := &request.HostConfig
	h.NetworkMode = "none"
	h.ReadonlyRootfs = true
	h.CapDrop = []string{"ALL"}
	h.SecurityOpt = []string{"no-new-privileges:true", candidateSeccompOption}
	h.Memory = int64(limits.MemoryMiB) << 20
	h.MemorySwap = h.Memory
	h.NanoCpus = int64(limits.CPUMillis) * 1000000
	h.CpuShares = appContainerCPUShares
	h.CpusetCpus = roots.AppCPUs
	h.PidsLimit = int64(limits.PIDs)
	h.ShmSize = 16 << 20
	h.CgroupnsMode = "private"
	h.IpcMode = "private"
	h.Init = true
	h.RestartPolicy.Name = "no"
	h.LogConfig.Type = "json-file"
	h.LogConfig.Config = map[string]string{"max-size": "1m", "max-file": "2"}
	h.Ulimits = []containerUlimit{{Name: "nofile", Soft: 1024, Hard: 1024}}
	h.Tmpfs = map[string]string{"/tmp": "rw,nosuid,nodev,noexec,size=16777216,mode=1777"}
	for _, m := range []struct {
		source, target string
		readOnly       bool
	}{{path.Join(roots.Data, owner.NamespaceID), "/data", true}, {httpDir, "/run/portal-app/http", false}, {brokerDir, "/run/portal-app/broker", true}} {
		mount := containerMount{Type: "bind", Source: m.source, Target: m.target, ReadOnly: m.readOnly}
		mount.BindOptions.Propagation = "rprivate"
		h.Mounts = append(h.Mounts, mount)
	}
	policy, err := json.Marshal(request)
	if err != nil {
		return empty, ErrInvalidResponse
	}
	request.Labels[containerLabelPrefix+"policy-sha256"] = containerHash(policy)
	body, err := json.Marshal(request)
	if err != nil || len(body) > 32768 {
		return empty, ErrInvalidResponse
	}
	return containerPlan{name: "portal-app-" + owner.ResourceID, body: body, bodySHA: containerHash(body), owner: owner, cpuset: roots.AppCPUs}, nil
}

// createCandidateContainerHTTP is deliberately package-private and unwired from
// the runner service. Its caller must already hold current admitted execution
// authority and pinned native mount/quota identities. A lost response never
// retries or adopts a same-named container. No start/remove action is implicit.
func createCandidateContainerHTTP(ctx context.Context, client *http.Client, plan containerPlan) (string, error) {
	if !validContainerPlan(plan) {
		return "", ErrInvalidResponse
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://app-runner-docker.invalid/v"+APIVersion+"/containers/create?"+url.Values{"name": []string{plan.name}}.Encode(), bytes.NewReader(plan.body))
	if err != nil {
		return "", ErrInvalidResponse
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", errors.Join(ErrContainerUncertain, ctx.Err())
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return "", ErrContainerConflict
	}
	if response.StatusCode != http.StatusCreated {
		failure := fmt.Errorf(
			"container create status %d: %w",
			response.StatusCode, ErrContainerUncertain)
		// The host lost a CPU after the app set was derived. Say so; the effect stays uncertain like any refusal.
		if body, _ := io.ReadAll(io.LimitReader(response.Body, 4096)); strings.Contains(string(body), "Requested CPUs are not available") {
			return "", errors.Join(ErrAppCPUsUnavailable, failure)
		}
		return "", failure
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	var created struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
	}
	if err != nil || len(raw) > 65536 || !validProjection(raw, &created) || json.Unmarshal(raw, &created) != nil || !containerSHA.MatchString(created.ID) || len(created.Warnings) != 0 {
		return "", errors.Join(ErrContainerUncertain, ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return "", errors.Join(ErrContainerUncertain, err)
	}
	return created.ID, nil
}
