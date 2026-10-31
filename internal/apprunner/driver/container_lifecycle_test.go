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

func lifecycleFixtureClient(t *testing.T, plan containerPlan, before, after string, status int, alter func(map[string]any)) (*http.Client, *[]string) {
	t.Helper()
	calls := []string{}
	client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if r.URL.Host != "app-runner-docker.invalid" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("lifecycle escaped local authority")
		}
		if r.Method == "POST" {
			if r.Body != nil {
				t.Fatal("lifecycle accepts no body/command")
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"fixture-sensitive"}`))}, nil
		}
		if r.Method != "GET" || r.URL.Path != "/v1.47/containers/"+strings.Repeat("c", 64)+"/json" || r.URL.RawQuery != "" {
			t.Fatal("unexpected lifecycle read", r.Method, r.URL)
		}
		v := containerInspectFixture(t, plan)
		state := before
		if len(calls) > 1 {
			state = after
		}
		v["State"].(map[string]any)["Status"] = state
		v["State"].(map[string]any)["Running"] = state == "running"
		if alter != nil {
			alter(v)
		}
		raw, _ := json.Marshal(v)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	}))
	return client, &calls
}
func TestCandidateLifecycleTransitionsAndReadOnlyIdempotence(t *testing.T) {
	cases := []struct {
		name, action, before, after, wantState string
		status, wantCalls                      int
		wantErr                                bool
	}{
		{"start created", "start", "created", "running", "running", 204, 3, false},
		{"already running", "start", "running", "running", "running", 204, 1, false},
		{"no auto restart", "start", "exited", "running", "", 204, 1, true},
		{"stop running", "stop", "running", "exited", "exited", 204, 3, false},
		{"already stopped", "stop", "exited", "exited", "exited", 204, 1, false},
		{"never started", "stop", "created", "created", "created", 204, 1, false},
		{"concurrent start", "start", "created", "running", "running", 304, 3, false},
		{"concurrent stop", "stop", "running", "exited", "exited", 304, 3, false},
		{"start exits", "start", "created", "exited", "", 204, 3, true},
		{"stop still running", "stop", "running", "running", "", 204, 3, true},
		{"server error", "start", "created", "running", "", 500, 2, true},
		{"redirect", "stop", "running", "exited", "", 302, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := containerFixture(t)
			client, calls := lifecycleFixtureClient(t, plan, tc.before, tc.after, tc.status, nil)
			var state string
			var err error
			if tc.action == "start" {
				state, err = startCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
			} else {
				state, err = stopCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
			}
			if (err != nil) != tc.wantErr || state != tc.wantState || len(*calls) != tc.wantCalls {
				t.Fatalf("lifecycle result: %s %v requests=%v", state, err, *calls)
			}
			if err != nil && strings.Contains(err.Error(), "fixture-sensitive") {
				t.Fatal("daemon error leaked")
			}
			if tc.wantCalls > 1 {
				expected := "POST /v1.47/containers/" + strings.Repeat("c", 64) + "/" + tc.action
				if tc.action == "stop" {
					expected += "?signal=SIGTERM&t=10"
				}
				if (*calls)[1] != expected {
					t.Fatal("unexpected lifecycle effect", *calls)
				}
			}
		})
	}
}
func TestCandidateLifecycleRefusesForeignPolicyBeforeMutation(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		t.Run(action, func(t *testing.T) {
			plan := containerFixture(t)
			client, calls := lifecycleFixtureClient(t, plan, "running", "exited", 204, func(v map[string]any) {
				v["Config"].(map[string]any)["Labels"].(map[string]any)[containerLabelPrefix+"installation"] = "foreign"
			})
			var err error
			if action == "start" {
				_, err = startCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
			} else {
				_, err = stopCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
			}
			if !errors.Is(err, ErrContainerConflict) || len(*calls) != 1 {
				t.Fatal("foreign lifecycle mutation", err, *calls)
			}
		})
	}
}

func TestCandidateLifecyclePostEffectAuthorityChangeIsUncertain(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		t.Run(action, func(t *testing.T) {
			plan := containerFixture(t)
			reads := 0
			before, after := "created", "running"
			if action == "stop" {
				before, after = "running", "exited"
			}
			client, calls := lifecycleFixtureClient(t, plan, before, after, 204, func(v map[string]any) {
				reads++
				if reads == 2 {
					v["Config"].(map[string]any)["Labels"].(map[string]any)[containerLabelPrefix+"control-fence"] = "99"
				}
			})
			var err error
			var state string
			if action == "start" {
				state, err = startCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
			} else {
				state, err = stopCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
			}
			if !errors.Is(err, ErrContainerUncertain) || state != "" || len(*calls) != 3 {
				t.Fatal("changed authority fabricated confirmed effect", state, err, *calls)
			}
		})
	}
}
func TestCandidateLifecycleLostAcknowledgementDoesNotRetry(t *testing.T) {
	plan := containerFixture(t)
	calls := 0
	client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method == "POST" {
			return nil, errors.New("synthetic lost acknowledgement")
		}
		v := containerInspectFixture(t, plan)
		raw, _ := json.Marshal(v)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	}))
	state, err := startCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
	if state != "" || !errors.Is(err, ErrContainerUncertain) || calls != 2 {
		t.Fatal("lost acknowledgement retried/adopted", state, err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	if _, err := startCandidateContainerHTTP(ctx, client, strings.Repeat("c", 64), plan); err == nil || calls != 0 {
		t.Fatal("canceled lifecycle accessed daemon", err, calls)
	}
}
