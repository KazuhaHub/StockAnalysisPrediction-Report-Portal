package driver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

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
type containerRoots struct{ Data, Sockets string }

// These package-private bytes are produced only by the closed policy builder.
// They are not signed execution authority. No production caller dispatches them
// until managed-command admission, native path pins and quota proofs are wired.
type containerPlan struct {
	name    string
	body    []byte
	bodySHA string
	owner   containerOwner
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
type containerHostConfig struct {
	NetworkMode                                         string
	ReadonlyRootfs, Privileged, AutoRemove              bool
	CapDrop, SecurityOpt                                []string
	Memory, MemorySwap, NanoCpus, PidsLimit, ShmSize    int64
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

func validContainerPlan(plan containerPlan) bool {
	if !validContainerOwner(plan.owner) || plan.name != "portal-app-"+plan.owner.ResourceID || len(plan.body) == 0 || len(plan.body) > 32768 || plan.bodySHA != containerHash(plan.body) {
		return false
	}
	var request containerCreate
	if json.Unmarshal(plan.body, &request) != nil {
		return false
	}
	for name, value := range ownerContainerLabels(plan.owner) {
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
	if candidateSeccompError != nil || err != nil || parsed.Manifest.Backend == nil || !validContainerOwner(owner) || parsed.SHA256 != owner.ReleaseSHA256 || image.ReleaseSHA256 != owner.ReleaseSHA256 || image.Reference != parsed.Manifest.Backend.Image || !nativeImageID.MatchString(image.ImageID) {
		return empty, ErrInvalidResponse
	}
	declared := false
	for _, platform := range parsed.Manifest.Backend.Platforms {
		if platform == image.Platform && (platform == "linux/amd64" || platform == "linux/arm64") {
			declared = true
		}
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
	return containerPlan{name: "portal-app-" + owner.ResourceID, body: body, bodySHA: containerHash(body), owner: owner}, nil
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
		return "", ErrContainerUncertain
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
