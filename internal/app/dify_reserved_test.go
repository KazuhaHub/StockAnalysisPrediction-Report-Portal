package app

// P39a: `_rp_*` inputs belong to the portal. They are never shown, never stored from a client's list,
// never submitted, and the names a workflow declares are recorded so a later release can inject only
// where the key exists.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// reservedDifyStub answers /info and /parameters with a mutable parameter list.
func reservedDifyStub(t *testing.T, params *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			fmt.Fprint(w, `{"name":"WF","mode":"workflow"}`)
		case "/parameters":
			fmt.Fprint(w, *params)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const paramsWithReserved = `{"user_input_form":[{"text-input":{"variable":"symbol","required":true}},{"text-input":{"variable":"_rp_owner_token","required":false}}]}`

func probeDify(t *testing.T, s *Server, baseURL string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	body := fmt.Sprintf(`{"base_url":%q,"api_key":"k"}`, baseURL)
	s.apiBatchDifyProbe(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("probe: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func inputNames(t *testing.T, raw any) []string {
	t.Helper()
	list, _ := raw.([]any)
	var out []string
	for _, v := range list {
		m, _ := v.(map[string]any)
		if name, _ := m["variable"].(string); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func TestReservedInputsNeverReachTheRunForm(t *testing.T) {
	s := batchServer(t)
	params := paramsWithReserved
	srv := reservedDifyStub(t, &params)

	out := probeDify(t, s, srv.URL)
	names := inputNames(t, out["inputs"])
	if len(names) != 1 || names[0] != "symbol" {
		t.Errorf("probe inputs = %v, want [symbol]", names)
	}
	reserved, _ := out["reserved_inputs"].([]any)
	if len(reserved) != 1 || reserved[0] != ownerTokenInput {
		t.Errorf("probe reserved_inputs = %v, want [%s]", out["reserved_inputs"], ownerTokenInput)
	}

	// A config stored before P39a may still carry it; the read path filters it.
	cfg, _ := json.Marshal(difyTargetConfig{
		BaseURL: srv.URL, APIKey: "k",
		Inputs: []dify.Input{{Variable: "symbol"}, {Variable: ownerTokenInput}},
	})
	id, err := s.st.CreateTarget(difyPluginSlug, "T", string(cfg))
	if err != nil {
		t.Fatal(err)
	}
	code, got := getTarget(t, s.apiBatchDifyTargetGet, id)
	if code != http.StatusOK {
		t.Fatalf("GET target: %d %v", code, got)
	}
	for _, name := range inputNames(t, got["inputs"]) {
		if isReservedInput(name) {
			t.Errorf("GET target returned %q", name)
		}
	}
	// The list the run form reads is filtered too.
	for _, in := range difyTargetInputs(string(cfg)) {
		if isReservedInput(in.Variable) {
			t.Errorf("difyTargetInputs returned %q", in.Variable)
		}
	}
}

func TestClientCannotSupplyAReservedInput(t *testing.T) {
	d := difyServer(t)
	defer d.Close()
	s := batchServer(t)
	post(t, s.apiBatchPluginImport, batchTestSpec)
	added := post(t, s.apiBatchTargetAdd, fmt.Sprintf(`{"plugin_slug":"p","name":"t","config":{"base_url":%q}}`, d.URL))
	targetID := int64(added["id"].(float64))

	created := post(t, s.apiBatchJobCreate, fmt.Sprintf(
		`{"target_id":%d,"rows":[{"code":"a","_rp_owner_token":"forged"}]}`, targetID))
	jobID := int64(created["job_id"].(float64))
	var inputs string
	if err := s.st.queryRow(`SELECT inputs FROM batch_items WHERE job_id=? ORDER BY id LIMIT 1`, jobID).Scan(&inputs); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inputs, "_rp_") {
		t.Errorf("a client-supplied reserved input was stored: %s", inputs)
	}

	// runInputs drops it for an internal user and replaces it for a restricted one.
	if _, ok := s.runInputs(BatchJob{CreatedBy: "staff"}, map[string]string{ownerTokenInput: "forged"})[ownerTokenInput]; ok {
		t.Error("an internal run forwarded a caller-supplied reserved input")
	}
	st := s.st
	root := st.EnsureDefaultGroup()
	org, _ := st.CreateUserGroup("ext-p39a", "", 0)
	st.SetGroupParent(org, root)
	st.SetGroupRestricted(org, true)
	st.UpsertUser(User{Username: "ext-p39a", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("ext-p39a", org)
	got := s.runInputs(BatchJob{CreatedBy: "ext-p39a"}, map[string]string{ownerTokenInput: "forged"})
	if got[ownerTokenInput] == "" || got[ownerTokenInput] == "forged" {
		t.Errorf("a restricted run kept the caller's reserved value: %q", got[ownerTokenInput])
	}
	waitForJobDone(t, s.st, jobID)
}

func TestProbeAndRefreshRecordReservedInputs(t *testing.T) {
	s := batchServer(t)
	params := paramsWithReserved
	srv := reservedDifyStub(t, &params)

	probe := probeDify(t, s, srv.URL)
	// Add the target from the probe's answer, exactly as the form does.
	addBody := fmt.Sprintf(`{"name":"T","base_url":%q,"api_key":"k","inputs":%s,"reserved_inputs":["%s"]}`,
		srv.URL, p39aJSON(t, probe["inputs"]), ownerTokenInput)
	added := post(t, s.apiBatchDifyTargetAdd, addBody)
	id := int64(added["id"].(float64))
	if got := difyTargetReserved(mustTargetConfig(t, s, id)); len(got) != 1 || got[0] != ownerTokenInput {
		t.Fatalf("stored reserved_inputs = %v", got)
	}

	// A preview reports them, and a confirm writes them back.
	rec := httptest.NewRecorder()
	s.apiBatchDifyRefresh(rec, httptest.NewRequest("POST", "/x", strings.NewReader(fmt.Sprintf(`{"ids":[%d]}`, id))), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Results []difyRefreshResult `json:"results"`
	}
	json.Unmarshal(rec.Body.Bytes(), &preview)
	if len(preview.Results) != 1 || len(preview.Results[0].ReservedInputs) != 1 {
		t.Fatalf("preview = %+v", preview.Results)
	}
	applyBody := fmt.Sprintf(`{"items":[{"id":%d,"inputs":%s,"reserved_inputs":["%s"]}]}`,
		id, p39aJSON(t, preview.Results[0].Inputs), ownerTokenInput)
	rec = httptest.NewRecorder()
	s.apiBatchDifyRefreshApply(rec, httptest.NewRequest("POST", "/x", strings.NewReader(applyBody)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	if got := difyTargetReserved(mustTargetConfig(t, s, id)); len(got) != 1 {
		t.Fatalf("apply lost reserved_inputs: %v", got)
	}

	// An editor save that does not touch the field keeps it.
	putBody := fmt.Sprintf(`{"name":"T","base_url":%q,"api_key":"","inputs":%s}`, srv.URL, p39aJSON(t, probe["inputs"]))
	if code := putTarget(t, s.apiBatchDifyTargetUpdate, id, putBody); code != http.StatusOK {
		t.Fatalf("update: %d", code)
	}
	if got := difyTargetReserved(mustTargetConfig(t, s, id)); len(got) != 1 {
		t.Fatalf("an editor save wiped reserved_inputs: %v", got)
	}

	// The workflow stops declaring it: the next preview says none, and a confirm clears it.
	params = `{"user_input_form":[{"text-input":{"variable":"symbol","required":true}}]}`
	rec = httptest.NewRecorder()
	s.apiBatchDifyRefresh(rec, httptest.NewRequest("POST", "/x", strings.NewReader(fmt.Sprintf(`{"ids":[%d]}`, id))), "admin")
	preview = struct {
		Results []difyRefreshResult `json:"results"`
	}{}
	json.Unmarshal(rec.Body.Bytes(), &preview)
	if len(preview.Results) != 1 || len(preview.Results[0].ReservedInputs) != 0 {
		t.Fatalf("preview after the declaration was dropped = %+v", preview.Results)
	}
	applyBody = fmt.Sprintf(`{"items":[{"id":%d,"inputs":%s,"reserved_inputs":[]}]}`,
		id, p39aJSON(t, preview.Results[0].Inputs))
	rec = httptest.NewRecorder()
	s.apiBatchDifyRefreshApply(rec, httptest.NewRequest("POST", "/x", strings.NewReader(applyBody)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("apply (clear): %d %s", rec.Code, rec.Body.String())
	}
	if got := difyTargetReserved(mustTargetConfig(t, s, id)); len(got) != 0 {
		t.Errorf("reserved_inputs survived the workflow dropping the declaration: %v", got)
	}
}

func mustTargetConfig(t *testing.T, s *Server, id int64) string {
	t.Helper()
	tgt, ok := s.st.GetTarget(id)
	if !ok {
		t.Fatal("target missing")
	}
	return tgt.Config
}

func p39aJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
