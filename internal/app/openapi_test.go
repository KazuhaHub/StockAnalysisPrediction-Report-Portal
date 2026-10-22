package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
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
// absence means (unattributed, never the client's to set).
func TestOpenAPIIngestRequestDocumentsOwnerToken(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	schema := spec["components"].(map[string]any)["schemas"].(map[string]any)["IngestRequest"].(map[string]any)

	desc := schema["properties"].(map[string]any)["owner_token"].(map[string]any)["description"].(string)
	for _, want := range []string{"归属", "受限 OU", "第一次有效的令牌"} {
		if !strings.Contains(desc, want) {
			t.Errorf("IngestRequest.owner_token (zh-CN) description = %q, missing %q", desc, want)
		}
	}
	i18n := schema["x-i18n"].(map[string]any)
	en := i18n["en-US"].(map[string]any)["properties"].(map[string]any)["owner_token"].(map[string]any)["description"].(string)
	for _, want := range []string{"OU-attribution token", "_rp_owner_token", "attributed to any OU", "restricted OU", "first valid token"} {
		if !strings.Contains(en, want) {
			t.Errorf("IngestRequest.owner_token (en-US) description = %q, missing %q", en, want)
		}
	}
	// zh-TW is machine-generated from zh-CN via OpenCC s2twp; it must exist and be non-empty.
	tw := i18n["zh-TW"].(map[string]any)["properties"].(map[string]any)["owner_token"].(map[string]any)["description"].(string)
	if strings.TrimSpace(tw) == "" {
		t.Error("IngestRequest.owner_token (zh-TW) description is empty")
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
