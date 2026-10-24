package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

func TestOpenAPIReadSchemasCoverWireFields(t *testing.T) {
	for _, lang := range []string{"zh-CN", "zh-TW", "en-US"} {
		t.Run(lang, func(t *testing.T) {
			data, err := localizedOpenAPIJSON(lang)
			if err != nil {
				t.Fatal(err)
			}
			var spec map[string]any
			if err := json.Unmarshal(data, &spec); err != nil {
				t.Fatal(err)
			}
			schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
			for _, tc := range []struct {
				name string
				wire reflect.Type
			}{
				{"PredictionReadSet", reflect.TypeOf(PredictionSet{})},
				{"PredictionReadMark", reflect.TypeOf(PredictionMark{})},
				{"PredictionReadClaim", reflect.TypeOf(PredictionClaim{})},
			} {
				props := schemas[tc.name].(map[string]any)["properties"].(map[string]any)
				for i := 0; i < tc.wire.NumField(); i++ {
					field := tc.wire.Field(i)
					if !field.IsExported() {
						continue
					}
					key := strings.Split(field.Tag.Get("json"), ",")[0]
					if key != "" && key != "-" && props[key] == nil {
						t.Errorf("%s misses wire field %s", tc.name, key)
					}
				}
			}
			setProps := schemas["PredictionReadSet"].(map[string]any)["properties"].(map[string]any)
			for _, obsolete := range []string{"counted_in_group", "baseline_bar", "baseline_fen"} {
				if setProps[obsolete] != nil {
					t.Errorf("obsolete field %s remains", obsolete)
				}
			}
			paths := spec["paths"].(map[string]any)
			for route, want := range map[string]string{"/api/v1/reports/{id}/predictions": "ReportPredictionReadResponse", "/api/v1/predictions": "PredictionReadResponse"} {
				response := paths[route].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)
				ref := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
				if ref != "#/components/schemas/"+want {
					t.Errorf("%s response schema = %v", route, ref)
				}
			}
		})
	}
}

// The embedded OpenAPI spec must be valid 3.1 JSON and cover every v1 endpoint
// (plus the public ones) so the served spec / rendered docs never drift.
func TestOpenAPISpecValid(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if spec["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v, want 3.1.0", spec["openapi"])
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing")
	}
	for _, p := range []string{
		"/api/v1/reports", "/api/v1/reports/{id}", "/api/v1/reports/manifest",
		"/api/v1/runs", "/api/v1/symbols", "/api/v1/tracking", "/api/v1/tracking/{id}",
		"/api/v1/now", "/healthz",
	} {
		if paths[p] == nil {
			t.Errorf("openapi.json missing path %s", p)
		}
	}
}

// The documented IngestRequest schema must match v1Ingest's actual runtime validation: date is
// always required, while the identity requires either symbol or title and the report type accepts
// either subtype or its rtype alias.
func TestOpenAPIIngestRequestMatchesRuntimeValidation(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	schema := spec["components"].(map[string]any)["schemas"].(map[string]any)["IngestRequest"].(map[string]any)
	required, _ := schema["required"].([]any)
	for _, r := range required {
		switch r {
		case "symbol", "title", "subtype", "rtype":
			t.Fatalf("IngestRequest.required unconditionally lists %q — no longer matches v1Ingest aliases/alternatives: %v", r, required)
		}
	}
	foundDate := false
	for _, r := range required {
		if r == "date" {
			foundDate = true
		}
	}
	if !foundDate {
		t.Errorf("IngestRequest.required missing date: %v", required)
	}
	// The accepted combinations must be expressed somewhere (anyOf is the OpenAPI 3.1 idiom here).
	anyOf, ok := schema["anyOf"].([]any)
	if !ok || len(anyOf) == 0 {
		t.Fatalf("IngestRequest has no anyOf constraint documenting identity/type alternatives")
	}
	seen := map[string]bool{}
	for _, clause := range anyOf {
		c := clause.(map[string]any)
		var parts []string
		for _, r := range c["required"].([]any) {
			parts = append(parts, r.(string))
		}
		seen[strings.Join(parts, "+")] = true
	}
	for _, want := range []string{"symbol+subtype", "symbol+rtype", "title+subtype", "title+rtype"} {
		if !seen[want] {
			t.Errorf("IngestRequest.anyOf missing %s branch: %v", want, anyOf)
		}
	}
}

// TestOpenAPIIngestRequestDocumentsEveryWireField keeps the two descriptions of the ingest body in
// step: the decoded Go struct (v1IngestRequest) and the documented IngestRequest schema. They are read
// by different audiences — a producer reads the spec to decide what to send — and nothing else compares
// them, so a field the handler accepts but the spec omits is a wire field a machine consumer cannot
// discover. owner_token was exactly that: accepted and verified at runtime, absent from the schema.
//
// Failure direction: FC (fail-closed on contract drift). A field present on one side and not the other
// fails the test in either direction, so it cannot be satisfied by deleting the documentation.
func TestOpenAPIIngestRequestDocumentsEveryWireField(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	schema := spec["components"].(map[string]any)["schemas"].(map[string]any)["IngestRequest"].(map[string]any)
	properties := schema["properties"].(map[string]any)

	// Reflect the struct's top-level json tags. Every field's tag must be a documented property.
	typ := reflect.TypeOf(v1IngestRequest{})
	wire := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			t.Fatalf("v1IngestRequest field %s has no usable json tag (%q)", typ.Field(i).Name, tag)
		}
		wire[name] = true
		if _, ok := properties[name]; !ok {
			t.Errorf("v1IngestRequest.%s (json:%q) is accepted at runtime but missing from IngestRequest.properties",
				typ.Field(i).Name, name)
		}
	}
	// And the reverse: a documented property the handler does not decode is a promise the portal breaks.
	for name := range properties {
		if !wire[name] {
			t.Errorf("IngestRequest.properties.%s is documented but v1IngestRequest does not decode it", name)
		}
	}
}

// TestOpenAPIIngestRequestDocumentsOwnerToken pins the specific field this PR exists for: the signed
// OU-attribution token is documented, in all three languages, with the sentence that says what its
// absence means (unattributed, never the client's to set) and who receives it. Since P39b that is
// every run with a submitter, so a description that still says "restricted OUs only" is a contract
// that lies to the workflow authors reading it.
func TestOpenAPIIngestRequestDocumentsOwnerToken(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	schema := spec["components"].(map[string]any)["schemas"].(map[string]any)["IngestRequest"].(map[string]any)

	desc := schema["properties"].(map[string]any)["owner_token"].(map[string]any)["description"].(string)
	for _, want := range []string{"归属", "每一次有发起人的运行", "第一次有效的令牌"} {
		if !strings.Contains(desc, want) {
			t.Errorf("IngestRequest.owner_token (zh-CN) description = %q, missing %q", desc, want)
		}
	}
	i18n := schema["x-i18n"].(map[string]any)
	en := i18n["en-US"].(map[string]any)["properties"].(map[string]any)["owner_token"].(map[string]any)["description"].(string)
	for _, want := range []string{"OU-attribution token", "_rp_owner_token", "attributed to any OU", "every run that has a submitter", "first valid token"} {
		if !strings.Contains(en, want) {
			t.Errorf("IngestRequest.owner_token (en-US) description = %q, missing %q", en, want)
		}
	}
	// zh-TW is machine-generated from zh-CN via OpenCC s2twp; it must exist and say the same thing.
	tw := i18n["zh-TW"].(map[string]any)["properties"].(map[string]any)["owner_token"].(map[string]any)["description"].(string)
	if strings.TrimSpace(tw) == "" {
		t.Error("IngestRequest.owner_token (zh-TW) description is empty")
	}
	if !strings.Contains(tw, "每一次有發起人的執行") {
		t.Errorf("IngestRequest.owner_token (zh-TW) description = %q, missing the every-run sentence", tw)
	}
	// The retired restricted-only claim must be gone from all three.
	for lang, text := range map[string]string{"zh-CN": desc, "en-US": en, "zh-TW": tw} {
		for _, stale := range []string{"restricted OU", "only into runs", "受限 OU", "只在"} {
			if strings.Contains(text, stale) {
				t.Errorf("IngestRequest.owner_token (%s) description still says %q: %q", lang, stale, text)
			}
		}
	}
}

// The source field is producer provenance, not an opaque author label: workflow versions are
// surfaced by the Portal from a stable `dify/<module>/<execution-version>` suffix. Keep the public
// contract explicit so a refreshed Dify API tool teaches new workflows the value that the reader
// actually consumes.
func TestOpenAPIDocumentsStructuredProducerSource(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	ingest := schemas["IngestRequest"].(map[string]any)
	properties := ingest["properties"].(map[string]any)
	source := properties["source"].(map[string]any)["description"].(string)
	for _, want := range []string{"dify/<模块>/<执行版本>", "V3.15.6", "识别并显示"} {
		if !strings.Contains(source, want) {
			t.Errorf("IngestRequest.source description = %q, missing %q", source, want)
		}
	}
	paths := spec["paths"].(map[string]any)
	getReports := paths["/api/v1/reports"].(map[string]any)["get"].(map[string]any)
	params := getReports["parameters"].([]any)
	for _, raw := range params {
		param := raw.(map[string]any)
		if param["name"] != "source" {
			continue
		}
		description := param["description"].(string)
		if !strings.Contains(description, "dify/1-6-4投资决策/V3.15.6") {
			t.Errorf("query source description = %q, missing structured-version example", description)
		}
		return
	}
	t.Fatal("GET /api/v1/reports is missing its source parameter")
}

func TestOpenAPILocalizedEndpoint(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/openapi.json?lang=en-US", nil)
	rec := httptest.NewRecorder()
	(&Server{}).apiOpenAPI(rec, req)
	if got := rec.Header().Get("Content-Language"); got != "en-US" {
		t.Fatalf("Content-Language = %q, want en-US", got)
	}
	if strings.Contains(rec.Body.String(), `"x-i18n"`) {
		t.Fatalf("localized response should strip x-i18n extensions")
	}
	var spec map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("localized openapi response is not valid JSON: %v", err)
	}
	info := spec["info"].(map[string]any)
	if got := info["title"]; got != "Research Report Portal · Dify Machine API (v1)" {
		t.Fatalf("localized info.title = %v", got)
	}
	post := spec["paths"].(map[string]any)["/api/v1/reports"].(map[string]any)["post"].(map[string]any)
	if got := post["summary"].(string); !strings.HasPrefix(got, "Ingest one report") {
		t.Fatalf("localized POST /api/v1/reports summary = %q", got)
	}
	params := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if params["$ref"] == nil {
		t.Fatalf("localized request schema lost its $ref: %v", params)
	}
}

// TestOpenAPIDocumentsEveryV1Route keeps the spec and the router from drifting apart. The spec calls
// itself "the single source of truth" for the machine API, which is only true while it lists what
// the router actually serves — and a route that exists but is undocumented is invisible to anyone
// generating a client from it, while one that is documented and gone is a promise the portal breaks.
//
// This has already happened once: v0.4.45 fixed a field the v1 read had always returned and the spec
// had never listed.
func TestOpenAPIDocumentsEveryV1Route(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("spec is not JSON: %v", err)
	}
	documented := map[string]bool{}
	for path, ops := range spec.Paths {
		for method := range ops {
			switch strings.ToLower(method) {
			case "get", "post", "put", "patch", "delete":
				documented[strings.ToUpper(method)+" "+path] = true
			}
		}
	}

	// The served routes come from the route table rather than from reading server.go for a spelling:
	// the registrations moved, the classes are what the registration PRODUCED, and a spec that drifts
	// from the router is the failure this test exists to catch.
	s := routeTableServer(t)
	s.wireRoutes(http.NewServeMux())
	var registered []string
	for _, row := range s.routeTable {
		if _, path, ok := strings.Cut(row.pattern, " "); ok && strings.HasPrefix(path, "/api/v1/") {
			registered = append(registered, row.pattern)
		}
	}
	if len(registered) < 8 {
		// A scan that silently matches nothing would pass every assertion below for ever.
		t.Fatalf("found only %d v1 routes; the scan is broken, not the spec", len(registered))
	}

	for _, pattern := range registered {
		if !documented[pattern] {
			t.Errorf("%s is served but not in openapi.json — a machine consumer cannot find it", pattern)
		}
		delete(documented, pattern)
	}
	// Whatever is left must be a deliberate non-v1 entry, not a route that has been removed.
	for route := range documented {
		if strings.Contains(route, "/api/v1") {
			t.Errorf("%s is documented but no longer served — the spec promises something gone", route)
		}
	}
}

// TestHealthzSpecMatchesTheHandler pins the one documented endpoint outside /api/v1. It gained a
// database check and a 503 in v0.4.45, and a spec that still described the old two-field answer
// would tell an orchestrator to treat an unreachable database as healthy.
func TestHealthzSpecMatchesTheHandler(t *testing.T) {
	var spec struct {
		Paths map[string]struct {
			Get struct {
				Responses map[string]json.RawMessage `json:"responses"`
			} `json:"get"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatal(err)
	}
	got := spec.Paths["/healthz"].Get.Responses
	for _, code := range []string{"200", "503"} {
		if _, ok := got[code]; !ok {
			t.Errorf("/healthz does not document a %s response, and the handler can return one", code)
		}
	}
	// The evaluator's probe (P9) has the same two outcomes and the same one-key body.
	got = spec.Paths["/healthz/forecast"].Get.Responses
	for _, code := range []string{"200", "503"} {
		if _, ok := got[code]; !ok {
			t.Errorf("/healthz/forecast does not document a %s response", code)
		}
	}

	// And the handler really does answer with both, so the assertion above is about something.
	st := newTestStore(t)
	s := &Server{st: st}
	if code, _ := healthGET(t, s); code != http.StatusOK {
		t.Fatalf("healthy → %d", code)
	}
	st.Close()
	s.health = healthCache{}
	if code, _ := healthGET(t, s); code != http.StatusServiceUnavailable {
		t.Fatalf("database down → %d", code)
	}
}

// TestOpenAPIPredictionEnumsMatchTheGoVocabulary keeps the spec and the validator from drifting apart.
//
// The two are read by different audiences — a producer reads the spec to decide what to send, the
// classifier decides what it will take — and nothing else compares them. A value added to one and not
// the other is either a claim refused despite the documentation, or a value the documentation invites
// and the portal throws away. Both are silent.
func TestOpenAPIPredictionEnumsMatchTheGoVocabulary(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)

	enumOf := func(schema string, path ...string) []string {
		cur, _ := schemas[schema].(map[string]any)
		for _, key := range path {
			cur, _ = cur[key].(map[string]any)
		}
		raw, _ := cur["enum"].([]any)
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			out = append(out, v.(string))
		}
		return out
	}

	for _, tc := range []struct {
		name   string
		got    []string
		want   []string
		schema string
	}{
		{"decision_status", enumOf("PredictionPayload", "properties", "decision_status"), predDecisionStatuses, "PredictionPayload.decision_status"},
		{"decision_quality", enumOf("PredictionPayload", "properties", "decision_quality"), predDecisionQualities, "PredictionPayload.decision_quality"},
		{"probability_method", enumOf("PredictionPayload", "properties", "probability_method"), predProbabilityMethods, "PredictionPayload.probability_method"},
		{"scenario_basis", enumOf("PredictionPayload", "properties", "scenario_basis"), predScenarioBases, "PredictionPayload.scenario_basis"},
		{"evidence.adoption", enumOf("PredictionPayload", "properties", "scenario_provenance", "properties", "evidence", "properties", "adoption"), predAdoptions, "PredictionPayload.scenario_provenance.evidence.adoption"},
		{"claim kind", enumOf("PredictionClaim", "properties", "kind"), predClaimKinds, "PredictionClaim.kind"},
		{"run_mode", enumOf("PredictionPayload", "properties", "run_mode"), predRunModes, "PredictionPayload.run_mode"},
		{"stance", enumOf("PredictionClaim", "properties", "stance"), predStances, "PredictionClaim.stance"},
		{"label", enumOf("PredictionClaim", "properties", "label"), predLabels, "PredictionClaim.label"},
		{"reader phase", enumOf("PredictionReadMark", "properties", "phase"), []string{predPhaseWaitingS0, predPhaseWaiting, predPhaseSettling, predPhaseRetrying, predPhaseOverdue, predPhaseResolved, predPhaseFinal}, "PredictionReadMark.phase"},
		{"reader direction", enumOf("PredictionReadMark", "properties", "direction"), []string{dirAgree, dirDisagree, dirFlat, dirUntested}, "PredictionReadMark.direction"},
		{"reader membership", enumOf("PredictionReadSet", "properties", "membership"), []string{membershipMember, membershipDuplicate, membershipDegraded, membershipMismatch, membershipPendingBaseline, membershipPreLive, membershipOffBenchmark, predStateTest, predStateBacktest, predStateAbsent, predStateRejected, membershipUnknown}, "PredictionReadSet.membership"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := append([]string(nil), tc.got...)
			want := append([]string(nil), tc.want...)
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s enum = %v, Go vocabulary = %v", tc.schema, tc.got, tc.want)
			}
		})
	}

	// The schema version and the price/probability patterns are the other half of the contract, and
	// the patterns are the ones a producer will copy into their own validation.
	if got := enumOf("PredictionPayload", "properties", "schema"); len(got) != 1 || got[0] != predSchemaV1 {
		t.Errorf("schema enum = %v, want [%s]", got, predSchemaV1)
	}
	pricePat, _ := schemas["PredictionPrice"].(map[string]any)["pattern"].(string)
	if pricePat != rePrice.String() {
		t.Errorf("PredictionPrice.pattern = %q, Go pattern = %q", pricePat, rePrice.String())
	}
	probPat, _ := schemas["PredictionProbability"].(map[string]any)["pattern"].(string)
	if probPat != reProb.String() {
		t.Errorf("PredictionProbability.pattern = %q, Go pattern = %q", probPat, reProb.String())
	}
}

// TestOpenAPIDocumentsForecastUnavailableOnPredictionReads ties the documented 403 on the two v1
// prediction reads to what the handlers answer. The 403 has two bodies: forecast_unavailable in the
// flat {error, code} envelope (an app-bridge token, a session at scope none) and enrolment_required
// in the v1 Error envelope (a cookie session held at the enrolment wall). The spec documents both as
// alternatives and every live body must match one of them.
func TestOpenAPIDocumentsForecastUnavailableOnPredictionReads(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	bridge := s.appTok.mint([]string{"query"}, time.Now())
	seedForecastReader(t, s, "doc-none", scopeNone, false)
	walled := seedForecastReader(t, s, "doc-walled", scopeAll, false)
	if err := s.st.SetGroupRequire2FA(walled, true); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	en := map[string]any{}
	if err := json.Unmarshal(openapiJSONForLang(t, "en-US"), &en); err != nil {
		t.Fatal(err)
	}
	for path, concrete := range map[string]string{
		"/api/v1/reports/{id}/predictions": "/api/v1/reports/1/predictions",
		"/api/v1/predictions":              "/api/v1/predictions?symbol=600160",
	} {
		get := spec["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
		resp, ok := get["responses"].(map[string]any)["403"].(map[string]any)
		if !ok {
			t.Fatalf("%s documents no 403 response", path)
		}
		alternatives, _ := resp["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["oneOf"].([]any)
		if len(alternatives) != 2 {
			t.Fatalf("%s: the 403 schema must list both bodies as oneOf, got %v", path, resp["content"])
		}
		var flatCode, nestedCode string
		for _, alt := range alternatives {
			props := alt.(map[string]any)["properties"].(map[string]any)
			if code, ok := props["code"].(map[string]any); ok {
				if props["error"].(map[string]any)["type"] != "string" {
					t.Errorf("%s: the flat 403 body must declare error as a string", path)
				}
				flatCode, _ = code["const"].(string)
			} else if e, ok := props["error"].(map[string]any); ok {
				if e["type"] != "object" || props["ok"].(map[string]any)["const"] != false {
					t.Errorf("%s: the v1 403 body must declare ok=false and an error object", path)
				}
				nestedCode, _ = e["properties"].(map[string]any)["code"].(map[string]any)["const"].(string)
			}
		}
		if flatCode != "forecast_unavailable" || nestedCode != "enrolment_required" {
			t.Fatalf("%s documents the wrong 403 codes: flat %q, v1 %q", path, flatCode, nestedCode)
		}
		base := resp["description"].(string)
		enGet := en["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
		enDesc := enGet["responses"].(map[string]any)["403"].(map[string]any)["description"].(string)
		for _, desc := range []string{base, enDesc} {
			if !strings.Contains(desc, flatCode) || !strings.Contains(desc, nestedCode) {
				t.Errorf("%s: the 403 description must name both codes: %q", path, desc)
			}
		}
		if !strings.ContainsFunc(base, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
			t.Errorf("%s: the base (zh-CN) 403 description is not Chinese: %q", path, base)
		}
		if enDesc == base {
			t.Errorf("%s: the en-US 403 description was not localized", path)
		}
		if !strings.Contains(get["description"].(string), "forecast_unavailable") {
			t.Errorf("%s: the operation description does not mention the bridge refusal", path)
		}
		// Every v1 change carries en-US and zh-TW blocks; the zh-TW text is OpenCC output, so it differs
		// from the zh-CN base but is never empty.
		for _, holder := range []map[string]any{get, resp} {
			twBlock, _ := holder["x-i18n"].(map[string]any)["zh-TW"].(map[string]any)
			if desc, _ := twBlock["description"].(string); desc == "" {
				t.Errorf("%s: a zh-TW description is missing", path)
			}
		}

		for _, tc := range []struct {
			who  string
			req  func() *http.Request
			flat bool
			code string
		}{
			{"bridge token", func() *http.Request {
				r := httptest.NewRequest("GET", concrete, nil)
				r.Header.Set("Authorization", "Bearer "+bridge)
				return r
			}, true, flatCode},
			{"session at none", func() *http.Request { return sessionForPath(t, s, "doc-none", "", concrete) }, true, flatCode},
			{"session held at the wall", func() *http.Request { return sessionForPath(t, s, "doc-walled", "", concrete) }, false, nestedCode},
		} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, tc.req())
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s as %s: %v", path, tc.who, err)
			}
			if w.Code != http.StatusForbidden || forecastMatrixErrorCode(t, w.Body.Bytes()) != tc.code {
				t.Fatalf("%s as %s answered %d %s; the spec documents 403 %s", path, tc.who, w.Code, w.Body.String(), tc.code)
			}
			_, errIsString := body["error"].(string)
			if errIsString != tc.flat {
				t.Errorf("%s as %s: body %s is in the wrong envelope (flat=%v)", path, tc.who, w.Body.String(), tc.flat)
			}
			if !tc.flat && body["ok"] != false {
				t.Errorf("%s as %s: the v1 envelope must carry ok=false: %s", path, tc.who, w.Body.String())
			}
		}
	}
}

func openapiJSONForLang(t *testing.T, lang string) []byte {
	t.Helper()
	out, err := localizedOpenAPIJSON(lang)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestOpenAPIDocumentsEditionLinksOnTheCookiePredictionRead ties the documented response of the
// per-report prediction read to what a browser session is actually answered. A cookie session at
// card or full stage gets two edition links beside the sets, and an integrator who reads the spec
// must be told the fields exist. Every top-level field of a live response has to be documented, and
// every field of a live link has to be a property of the documented link.
func TestOpenAPIDocumentsEditionLinksOnTheCookiePredictionRead(t *testing.T) {
	eachReaderServer(t, func(t *testing.T, s *Server) {
		s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
		fx := seedEditionFixture(t, s)
		grantSiblingEdition(t, s)
		path := "/api/v1/reports/" + itoa64(fx.current) + "/predictions"
		r := sessionForPath(t, s, "edition-all", "GET /api/v1/reports/{id}/predictions", path)
		r.SetPathValue("id", itoa64(fx.current))
		w := httptest.NewRecorder()
		s.v1ReportPredictions(w, r)
		var live map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &live); err != nil || w.Code != http.StatusOK {
			t.Fatalf("cookie read: %d %s: %v", w.Code, w.Body.String(), err)
		}
		for _, field := range []string{"sibling", "counted_edition"} {
			if _, ok := live[field].(map[string]any); !ok {
				t.Fatalf("the fixture no longer produces %s on the cookie read: %s", field, w.Body.String())
			}
		}
		for _, lang := range []string{"zh-CN", "zh-TW", "en-US"} {
			var spec map[string]any
			if err := json.Unmarshal(openapiJSONForLang(t, lang), &spec); err != nil {
				t.Fatal(err)
			}
			schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
			documented := map[string]bool{}
			response := schemas["ReportPredictionReadResponse"].(map[string]any)
			for _, part := range response["allOf"].([]any) {
				part := part.(map[string]any)
				if ref, ok := part["$ref"].(string); ok {
					for name := range schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)["properties"].(map[string]any) {
						documented[name] = true
					}
				}
				if props, ok := part["properties"].(map[string]any); ok {
					for name := range props {
						documented[name] = true
					}
				}
			}
			for field := range live {
				if !documented[field] {
					t.Errorf("%s: the cookie read returns %q, which the spec does not document", lang, field)
				}
			}
			edition, ok := schemas["PredictionReadEdition"].(map[string]any)
			if !ok {
				t.Errorf("%s: the spec has no PredictionReadEdition schema", lang)
				continue
			}
			props := edition["properties"].(map[string]any)
			for field := range live["sibling"].(map[string]any) {
				if props[field] == nil {
					t.Errorf("%s: an edition link returns %q, which the spec does not document", lang, field)
				}
			}
			for i := 0; i < reflect.TypeOf(forecastReadingEdition{}).NumField(); i++ {
				field := strings.Split(reflect.TypeOf(forecastReadingEdition{}).Field(i).Tag.Get("json"), ",")[0]
				if props[field] == nil {
					t.Errorf("%s: the edition link schema misses wire field %s", lang, field)
				}
				if !containsStr(toStrings(edition["required"]), field) {
					t.Errorf("%s: the edition link schema does not require %s", lang, field)
				}
			}
			for _, field := range []string{"sibling", "counted_edition"} {
				if ref := responseProperty(schemas, field)["$ref"]; ref != "#/components/schemas/PredictionReadEdition" {
					t.Errorf("%s: %s is documented as %v", lang, field, ref)
				}
			}
			op := spec["paths"].(map[string]any)["/api/v1/reports/{id}/predictions"].(map[string]any)["get"].(map[string]any)
			for _, field := range []string{"sibling", "counted_edition"} {
				if !strings.Contains(op["description"].(string), field) {
					t.Errorf("%s: the operation description does not mention %s", lang, field)
				}
			}
		}
	})
}

func toStrings(raw any) []string {
	var out []string
	for _, v := range raw.([]any) {
		out = append(out, v.(string))
	}
	return out
}

// responseProperty finds one property of the per-report response across its allOf parts.
func responseProperty(schemas map[string]any, name string) map[string]any {
	for _, part := range schemas["ReportPredictionReadResponse"].(map[string]any)["allOf"].([]any) {
		if props, ok := part.(map[string]any)["properties"].(map[string]any); ok {
			if prop, ok := props[name].(map[string]any); ok {
				return prop
			}
		}
	}
	return map[string]any{}
}
