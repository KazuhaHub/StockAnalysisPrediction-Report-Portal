package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func freshInspectionMemo(t *testing.T) {
	t.Helper()
	old := inspectionMemo
	inspectionMemo = newDecisionMemo[containerStateInspection](64)
	t.Cleanup(func() { inspectionMemo = old })
}

func runningInspectFixture(t *testing.T, plan containerPlan) map[string]any {
	t.Helper()
	v := containerInspectFixture(t, plan)
	v["State"] = map[string]any{"Status": "running", "Running": true, "Paused": false, "Restarting": false, "Dead": false, "Pid": 4242, "StartedAt": "2026-11-14T00:00:00.000000000Z"}
	return v
}

// countingDaemon answers every inspect with the document the test currently holds and counts the calls.
func countingDaemon(t *testing.T, doc *map[string]any, calls *int) *http.Client {
	t.Helper()
	return daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.Method != "GET" {
			t.Fatal("observation mutated", r.Method)
		}
		raw, _ := json.Marshal(*doc)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	}))
}

// The relayed request path inspects the same container before and after its IO, in two steps. An identical
// document under the same plan has the same verdict, so only the first inspection may validate it. The live
// document is still fetched every time.
func TestInspectionOfAnIdenticalDocumentIsValidatedOnce(t *testing.T) {
	freshInspectionMemo(t)
	plan := containerFixture(t)
	doc := runningInspectFixture(t, plan)
	calls := 0
	client := countingDaemon(t, &doc, &calls)
	for i := 0; i < 4; i++ {
		state, err := inspectCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
		if err != nil || state != "running" {
			t.Fatal("inspection", i, state, err)
		}
	}
	if calls != 4 {
		t.Fatalf("every inspection must still ask the daemon: %d calls", calls)
	}
	if hits, misses, _ := inspectionMemo.stats(); hits != 3 || misses != 1 {
		t.Fatalf("identical documents must be validated once: hits=%d misses=%d", hits, misses)
	}
}

// With the memory warm from an acceptable document, every document that differs in anything the verdict
// depends on is refused exactly as it is cold: replaced container, changed policy label, changed limits,
// namespace or mount drift, a different plan, and a state that is not running.
func TestWarmInspectionRefusesEveryDriftColdInspectionRefuses(t *testing.T) {
	plan := containerFixture(t)
	cases := map[string]func(map[string]any){
		"replaced container id": func(v map[string]any) { v["Id"] = strings.Repeat("d", 64) },
		"name":                  func(v map[string]any) { v["Name"] = "/other" },
		"image":                 func(v map[string]any) { v["Image"] = "sha256:" + strings.Repeat("d", 64) },
		"policy label": func(v map[string]any) {
			v["Config"].(map[string]any)["Labels"].(map[string]any)[containerLabelPrefix+"policy-sha256"] = strings.Repeat("0", 64)
		},
		"generation label": func(v map[string]any) {
			v["Config"].(map[string]any)["Labels"].(map[string]any)[containerLabelPrefix+"generation"] = "99"
		},
		"foreign label": func(v map[string]any) {
			v["Config"].(map[string]any)["Labels"].(map[string]any)[containerLabelPrefix+"extra"] = "foreign"
		},
		"memory limit":    func(v map[string]any) { v["HostConfig"].(map[string]any)["Memory"] = 1 << 40 },
		"privileged":      func(v map[string]any) { v["HostConfig"].(map[string]any)["Privileged"] = true },
		"capability":      func(v map[string]any) { v["HostConfig"].(map[string]any)["CapAdd"] = []string{"SYS_ADMIN"} },
		"namespace mount": func(v map[string]any) { v["Mounts"].([]any)[0].(map[string]any)["Source"] = "/srv/other" },
		"mount writable":  func(v map[string]any) { v["Mounts"].([]any)[0].(map[string]any)["RW"] = true },
		"extra mount": func(v map[string]any) {
			v["Mounts"] = append(v["Mounts"].([]any), map[string]any{"Type": "bind", "Source": "/", "Destination": "/host", "RW": true})
		},
		"state alias":  func(v map[string]any) { v["State"].(map[string]any)["running"] = true },
		"state paused": func(v map[string]any) { v["State"].(map[string]any)["Paused"] = true },
		"state exited": func(v map[string]any) {
			s := v["State"].(map[string]any)
			s["Status"], s["Running"] = "exited", false
		},
		"state mismatch": func(v map[string]any) { v["State"].(map[string]any)["Running"] = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			verdict := func(warm bool) (string, error) {
				freshInspectionMemo(t)
				good := runningInspectFixture(t, plan)
				calls := 0
				if warm {
					if state, err := inspectCandidateContainerHTTP(context.Background(), countingDaemon(t, &good, &calls), strings.Repeat("c", 64), plan); err != nil || state != "running" {
						t.Fatal("the warm-up document was refused", err)
					}
				}
				bad := runningInspectFixture(t, plan)
				mutate(bad)
				return inspectCandidateContainerHTTP(context.Background(), countingDaemon(t, &bad, &calls), strings.Repeat("c", 64), plan)
			}
			coldState, coldErr := verdict(false)
			warmState, warmErr := verdict(true)
			if name == "state exited" {
				if coldErr != nil || warmErr != nil || coldState != "exited" || warmState != "exited" {
					t.Fatal("an exited container must read as exited", coldState, coldErr, warmState, warmErr)
				}
				return
			}
			if coldErr == nil || warmErr == nil {
				t.Fatalf("drift accepted: cold=%v warm=%v", coldErr, warmErr)
			}
			for _, target := range []error{ErrContainerConflict, ErrContainerUncertain} {
				if errors.Is(coldErr, target) != errors.Is(warmErr, target) {
					t.Fatalf("warm verdict differs from cold: cold=%v warm=%v", coldErr, warmErr)
				}
			}
		})
	}
}

// A refusal is never remembered, and the plan is part of the key: the same bytes under another plan (or for
// another native id) are a different question.
func TestInspectionMemoNeverStoresRefusalsAndIsKeyedByPlan(t *testing.T) {
	freshInspectionMemo(t)
	plan := containerFixture(t)
	doc := runningInspectFixture(t, plan)
	doc["HostConfig"].(map[string]any)["Privileged"] = true
	calls := 0
	client := countingDaemon(t, &doc, &calls)
	for i := 0; i < 3; i++ {
		if _, err := inspectCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan); !errors.Is(err, ErrContainerConflict) {
			t.Fatal("a refusal changed its verdict", err)
		}
	}
	if hits, misses, size := inspectionMemo.stats(); hits != 0 || misses != 3 || size != 0 {
		t.Fatalf("a refusal was remembered: hits=%d misses=%d size=%d", hits, misses, size)
	}
	raw := []byte(`{"Id":"x"}`)
	other := plan
	other.bodySHA = strings.Repeat("0", 64)
	if inspectionKey(plan, strings.Repeat("c", 64), raw) == inspectionKey(other, strings.Repeat("c", 64), raw) ||
		inspectionKey(plan, strings.Repeat("c", 64), raw) == inspectionKey(plan, strings.Repeat("d", 64), raw) ||
		inspectionKey(plan, strings.Repeat("c", 64), raw) == inspectionKey(plan, strings.Repeat("c", 64), append(raw, ' ')) {
		t.Fatal("the key ignores plan, native id or document bytes")
	}
}
