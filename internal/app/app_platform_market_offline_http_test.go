package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformOfflineEnvelope(
	t *testing.T,
	descriptor, signature, frontend []byte,
) []byte {
	t.Helper()
	var manifest appplatform.Manifest
	if err := json.Unmarshal(descriptor, &manifest); err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	for name, body := range map[string][]byte{
		"descriptor.json": descriptor,
		"descriptor.sig":  signature,
		"blobs/" + manifest.Frontend.SHA256 + "/" +
			path.Base(manifest.Frontend.Artifact): frontend,
	} {
		header := &zip.FileHeader{Name: name}
		header.SetMode(0644)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func platformOfflineRequest(
	mux *http.ServeMux,
	cookie string,
	contentType string,
	body []byte,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/app-platform/market/offline",
		bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func testPlatformOfflineMarketHTTP(t *testing.T, pg bool) {
	s, actor := platformTrustTestServer(t, pg)
	platformRegisterTestPublisher(t, s, actor)
	descriptor, _, frontend := platformSignedReleaseFixture(
		t, "1.0.0-beta.4")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(descriptor, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	var err error
	descriptor, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := platformSignTestDescriptor(t, descriptor)
	envelope := platformOfflineEnvelope(
		t, descriptor, signature, frontend)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(
		*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	response := platformOfflineRequest(
		mux, cookie, "application/zip", envelope)
	var staged struct {
		Release   string `json:"release_sha256"`
		AppID     string `json:"app_id"`
		Publisher string `json:"publisher_id"`
		Reused    bool   `json:"reused"`
		Format    string `json:"envelope_format"`
		Execution string `json:"execution"`
	}
	if response.Code != http.StatusOK ||
		json.Unmarshal(response.Body.Bytes(), &staged) != nil ||
		staged.Release != platformDigest(descriptor) ||
		staged.AppID != "notes-sample" ||
		staged.Publisher != "fixture-publisher" ||
		staged.Reused ||
		staged.Format != "portal.offline-envelope/1-dev.1" ||
		staged.Execution != "static" {
		t.Fatal("offline package staging", response.Code,
			response.Body.String())
	}
	if scalar[int](t, s.st, `
		SELECT COUNT(*) FROM app_platform_releases
		WHERE sha256=?`, staged.Release) != 1 ||
		scalar[int](t, s.st, `
			SELECT COUNT(*) FROM app_platform_frontend_artifacts
			WHERE sha256=(SELECT frontend_sha256
			  FROM app_platform_releases WHERE sha256=?)`,
			staged.Release) != 1 {
		t.Fatal("offline package did not persist exact release bytes")
	}
	repeated := platformOfflineRequest(
		mux, cookie, "application/zip", envelope)
	if repeated.Code != http.StatusOK ||
		!bytes.Contains(repeated.Body.Bytes(), []byte(`"reused":true`)) {
		t.Fatal("offline idempotent replay", repeated.Code,
			repeated.Body.String())
	}
	plan := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/plans",
		cookie, platformManagementPlanBody(staged.Release, actor.username))
	if plan.Code != http.StatusOK {
		t.Fatal("offline install review", plan.Code, plan.Body.String())
	}
	var review struct {
		ID  string `json:"plan_id"`
		SHA string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	commit := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/operations",
		cookie, fmt.Sprintf(
			`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`,
			review.ID, review.SHA, platformFixtureID(997)))
	if commit.Code != http.StatusAccepted {
		t.Fatal("offline install commit", commit.Code, commit.Body.String())
	}
	var admitted struct {
		Installation string `json:"installation_id"`
	}
	if err := json.Unmarshal(commit.Body.Bytes(), &admitted); err != nil ||
		!platformValidUUID(admitted.Installation) {
		t.Fatal("offline install admission", commit.Body.String(), err)
	}
	platformStartFrontendService(t, s)
	platformAwaitFrontendService(t, s, "complete")
	if _, err := s.inspectPlatformLaunchAccess(
		actor, admitted.Installation); err != nil {
		t.Fatal("offline-installed static app cannot launch", err)
	}
}

func TestPlatformOfflineMarketHTTPSQLite(t *testing.T) {
	testPlatformOfflineMarketHTTP(t, false)
}

func TestPlatformOfflineMarketHTTPPostgres(t *testing.T) {
	testPlatformOfflineMarketHTTP(t, true)
}

func TestPlatformOfflineMarketHTTPRefusals(t *testing.T) {
	s, actor := platformTrustTestServer(t, false)
	platformRegisterTestPublisher(t, s, actor)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(
		*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	descriptor, signature, frontend := platformSignedReleaseFixture(
		t, "1.0.0-beta.5")
	envelope := platformOfflineEnvelope(
		t, descriptor, signature, frontend)
	managedDescriptor, managedFrontend := platformManagedReleaseFixture(
		t, "linux/amd64")
	managedEnvelope := platformOfflineEnvelope(
		t, managedDescriptor, platformSignTestDescriptor(t, managedDescriptor),
		managedFrontend)
	for name, test := range map[string]struct {
		cookie      string
		contentType string
		body        []byte
		status      int
	}{
		"json content type": {
			cookie: cookie, contentType: "application/json",
			body: envelope, status: http.StatusUnsupportedMediaType,
		},
		"malformed": {
			cookie: cookie, contentType: "application/zip",
			body: []byte("not a ZIP"), status: http.StatusBadRequest,
		},
		"managed without image payload": {
			cookie: cookie, contentType: "application/zip",
			body: managedEnvelope, status: http.StatusConflict,
		},
		"anonymous": {
			contentType: "application/zip", body: envelope,
			status: http.StatusUnauthorized,
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := platformOfflineRequest(
				mux, test.cookie, test.contentType, test.body)
			if response.Code != test.status {
				t.Fatal("offline refusal", response.Code,
					response.Body.String())
			}
		})
	}
	if scalar[int](t, s.st, `
		SELECT COUNT(*) FROM app_platform_releases`) != 0 ||
		scalar[int](t, s.st, `
			SELECT COUNT(*) FROM app_platform_frontend_artifacts`) != 0 ||
		scalar[int](t, s.st, `
			SELECT COUNT(*) FROM app_platform_installations`) != 0 {
		t.Fatal("refused offline package wrote release or authority")
	}
}
