package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const fixtureEventStatePath = "/data/.portal-event-state.json"

var errFixtureEventIdentity = errors.New("fixture event identity changed")

type fixtureEventHandler struct {
	mu      sync.Mutex
	path    string
	dataDir string
	state   fixtureEventState
}

type fixtureEventState struct {
	Format    string                       `json:"format"`
	Processed map[string]string            `json:"processed"`
	Resources map[string]fixtureEventOrder `json:"resources"`
	Dropped   map[string]bool              `json:"dropped"`
}

type fixtureEventOrder struct {
	OccurredMS int64  `json:"occurred_ms"`
	EventID    string `json:"event_id"`
}

type fixtureEventResult struct {
	Decision     string `json:"decision"`
	EventID      string `json:"event_id"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
}

func newFixtureEventHandler(path, dataDir string) (*fixtureEventHandler, error) {
	h := &fixtureEventHandler{
		path: path, dataDir: dataDir,
		state: fixtureEventState{
			Format:    "portal.fixture.event-state/1-dev.1",
			Processed: map[string]string{},
			Resources: map[string]fixtureEventOrder{},
			Dropped:   map[string]bool{},
		},
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	var state fixtureEventState
	if json.Unmarshal(raw, &state) != nil ||
		state.Format != h.state.Format ||
		len(state.Processed) > 512 ||
		len(state.Resources) > 512 ||
		len(state.Dropped) > 512 {
		return nil, errFixtureEventIdentity
	}
	if state.Processed == nil {
		state.Processed = map[string]string{}
	}
	if state.Resources == nil {
		state.Resources = map[string]fixtureEventOrder{}
	}
	if state.Dropped == nil {
		state.Dropped = map[string]bool{}
	}
	h.state = state
	return h, nil
}

func (h *fixtureEventHandler) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != appplatform.TaskEventPath ||
		r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		fixtureTaskFailure(w, "event_delivery_invalid")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, appplatform.MaxTaskBackendBody+1))
	defer clear(body)
	if err != nil || len(body) == 0 || len(body) > appplatform.MaxTaskBackendBody {
		fixtureTaskFailure(w, "event_delivery_invalid")
		return
	}
	delivery, err := appplatform.ParseEventDelivery(body)
	if err != nil {
		fixtureTaskFailure(w, "event_delivery_invalid")
		return
	}
	registration, err := fixtureRegistration()
	if err != nil {
		fixtureTaskFailure(w, "event_registration_unavailable")
		return
	}
	defer registration.Close()
	metadata, err := registration.Metadata()
	if err != nil || validateFixtureServiceIdentity(
		r.Header.Get(appplatform.TaskServiceIdentityHeader),
		metadata.Identity, delivery) != nil {
		fixtureTaskFailure(w, "event_service_identity_invalid")
		return
	}
	decision, drop, err := h.apply(delivery, body)
	if errors.Is(err, errFixtureEventIdentity) {
		fixtureTaskFailure(w, "event_identity_conflict")
		return
	}
	if err != nil {
		fixtureTaskFailure(w, "event_state_unavailable")
		return
	}
	if drop {
		if hijacker, ok := w.(http.Hijacker); ok {
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	fixtureTaskSuccess(w, fixtureEventResult{
		Decision: decision, EventID: delivery.EventID,
		ResourceType: delivery.ResourceType, ResourceID: delivery.ResourceID,
	})
}

func (h *fixtureEventHandler) apply(delivery appplatform.EventDelivery, body []byte) (string, bool, error) {
	digest := sha256.Sum256(body)
	payloadSHA := hex.EncodeToString(digest[:])
	h.mu.Lock()
	defer h.mu.Unlock()
	if previous, ok := h.state.Processed[delivery.EventID]; ok {
		if previous != payloadSHA {
			return "", false, errFixtureEventIdentity
		}
		return "duplicate", false, nil
	}
	if len(h.state.Processed) >= 512 {
		return "", false, errors.New("fixture event capacity exhausted")
	}
	occurred, err := strconv.ParseInt(delivery.OccurredMS, 10, 64)
	if err != nil {
		return "", false, errFixtureEventIdentity
	}
	resource := delivery.ResourceType + ":" + delivery.ResourceID
	decision := "applied"
	if previous, ok := h.state.Resources[resource]; ok &&
		appplatform.EventDeliveryOrder(delivery, appplatform.EventDelivery{
			OccurredMS: strconv.FormatInt(previous.OccurredMS, 10),
			EventID:    previous.EventID,
		}) < 0 {
		decision = "ignored_older"
	}
	h.state.Processed[delivery.EventID] = payloadSHA
	if decision == "applied" {
		h.state.Resources[resource] = fixtureEventOrder{
			OccurredMS: occurred, EventID: delivery.EventID,
		}
	}
	drop := fixtureDropOnce(delivery.Payload) && !h.state.Dropped[delivery.EventID]
	if drop {
		h.state.Dropped[delivery.EventID] = true
	}
	if err := h.persist(); err != nil {
		return "", false, err
	}
	return decision, drop, nil
}

func (h *fixtureEventHandler) persist() error {
	raw, err := json.Marshal(h.state)
	if err != nil || len(raw) > 65536 {
		return errFixtureEventIdentity
	}
	temporary := h.path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, h.path); err != nil {
		return err
	}
	dir, err := os.Open(h.dataDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func fixtureRegistration() (appplatform.BackendRegistration, error) {
	file, err := os.Open("/run/portal-app/broker/registration.json")
	if err != nil {
		return appplatform.BackendRegistration{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	defer clear(raw)
	if err != nil || len(raw) > 4096 {
		return appplatform.BackendRegistration{}, errFixtureEventIdentity
	}
	return appplatform.ParseBackendRegistration(raw)
}

func validateFixtureServiceIdentity(
	raw string, identity appplatform.BackendIdentity, delivery appplatform.EventDelivery,
) error {
	if len(raw) == 0 || len(raw) > 8192 {
		return errFixtureEventIdentity
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var grant appplatform.ServiceIdentityGrant
	if decoder.Decode(&grant) != nil {
		return errFixtureEventIdentity
	}
	if decoder.Decode(&struct{}{}) != io.EOF ||
		grant.Format != appplatform.ServiceIdentityFormat ||
		grant.AppID != identity.AppID ||
		grant.InstallationID != identity.InstallationID ||
		grant.Generation != identity.Generation ||
		grant.PolicyRevision != identity.PolicyRevision ||
		grant.ControllerEpoch != identity.ControllerEpoch ||
		grant.ReleaseSHA256 != identity.ReleaseSHA256 ||
		len(grant.Mac) != 64 ||
		!fixtureTaskResource(grant.Resources, "events.subscribe") {
		return errFixtureEventIdentity
	}
	if delivery.EventType == appplatform.EventReportIngested &&
		!fixtureTaskResource(grant.Resources, "reports.read") {
		return errFixtureEventIdentity
	}
	now, issued, expires := time.Now().UnixMilli(),
		int64Value(grant.IssuedMS), int64Value(grant.ExpiresMS)
	if issued <= 0 || expires <= now || expires-issued > int64(5*time.Minute/time.Millisecond) {
		return errFixtureEventIdentity
	}
	return nil
}

func fixtureTaskResource(resources []string, required string) bool {
	previous, found := "", false
	for _, resource := range resources {
		if resource == "" || previous >= resource {
			return false
		}
		previous = resource
		found = found || resource == required
	}
	return found
}

func fixtureDropOnce(payload json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return false
	}
	var drop bool
	return json.Unmarshal(fields["drop_once"], &drop) == nil && drop
}

func int64Value(value string) int64 {
	out, _ := strconv.ParseInt(value, 10, 64)
	return out
}

func fixtureTaskSuccess(w http.ResponseWriter, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		fixtureTaskFailure(w, "event_result_invalid")
		return
	}
	encoded, err := appplatform.EncodeTaskBackendResponse(
		appplatform.TaskExecutionResult{
			Outcome: appplatform.TaskResultSucceeded, ResultJSON: raw,
		})
	if err != nil {
		fixtureTaskFailure(w, "event_result_invalid")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func fixtureTaskFailure(w http.ResponseWriter, code string) {
	encoded, err := appplatform.EncodeTaskBackendResponse(
		appplatform.TaskExecutionResult{
			Outcome: appplatform.TaskResultFailed, ErrorCode: code,
		})
	if err != nil {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}
