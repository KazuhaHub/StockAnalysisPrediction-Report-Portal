package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postSettingsJSON(t *testing.T, s *Server, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal settings payload: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/admin/settings", strings.NewReader(string(raw)))
	rec := httptest.NewRecorder()
	s.apiSettingsSave(rec, req, "admin")
	return rec
}

func publicSiteSettings(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiSite(rec, httptest.NewRequest("GET", "/api/site", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("apiSite status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode apiSite: %v", err)
	}
	return out
}

func TestSiteSettingsSplitPagesDoNotClobberEachOther(t *testing.T) {
	s := newV1Server(t)

	rec := postSettingsJSON(t, s, map[string]any{
		"timezone":            "Asia/Shanghai",
		"siteTitle":           " 智研平台 ",
		"siteLogoUrl":         "/brand/logo.png",
		"footerText":          " 备案信息 ",
		"footerShowInfo":      false,
		"footerShowVersion":   false,
		"pwaEnabled":          false,
		"pwaIconUrl":          "/brand/app.png",
		"announcementEnabled": true,
		"announcementPopup":   true,
		"announcementLevel":   "error",
		"announcementTitle":   " 原公告 ",
		"announcementContent": " 原正文 ",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("initial save status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = postSettingsJSON(t, s, map[string]any{
		"announcementEnabled": false,
		"announcementTitle":   " 新公告 ",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("announcement-only save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting("site_title", ""); got != "智研平台" {
		t.Fatalf("announcement-only save clobbered site_title: %q", got)
	}
	if got := s.st.GetSetting("timezone", ""); got != "Asia/Shanghai" {
		t.Fatalf("announcement-only save clobbered timezone: %q", got)
	}
	if got := s.st.GetSetting("footer_text", ""); got != "备案信息" {
		t.Fatalf("announcement-only save clobbered footer_text: %q", got)
	}
	if settingBool(s.st.GetSetting("footer_show_info", ""), true) {
		t.Fatalf("announcement-only save clobbered footer_show_info")
	}
	if settingBool(s.st.GetSetting("pwa_enabled", ""), true) {
		t.Fatalf("announcement-only save clobbered pwa_enabled")
	}
	if settingBool(s.st.GetSetting("announcement_enabled", ""), true) {
		t.Fatalf("announcement_enabled was not updated")
	}
	if !settingBool(s.st.GetSetting("announcement_popup", ""), false) ||
		s.st.GetSetting("announcement_level", "") != "error" ||
		s.st.GetSetting("announcement_title", "") != "新公告" ||
		s.st.GetSetting("announcement_content", "") != "原正文" {
		t.Fatalf("omitted announcement fields were not preserved")
	}

	rec = postSettingsJSON(t, s, map[string]any{
		"siteTitle":      " 运营面板 ",
		"footerShowInfo": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("site-only save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting("site_title", ""); got != "运营面板" {
		t.Fatalf("site_title not updated: %q", got)
	}
	if !settingBool(s.st.GetSetting("footer_show_info", ""), false) {
		t.Fatalf("footer_show_info not updated")
	}
	if settingBool(s.st.GetSetting("announcement_enabled", ""), true) ||
		!settingBool(s.st.GetSetting("announcement_popup", ""), false) ||
		s.st.GetSetting("announcement_level", "") != "error" ||
		s.st.GetSetting("announcement_title", "") != "新公告" ||
		s.st.GetSetting("announcement_content", "") != "原正文" {
		t.Fatalf("site-only save clobbered announcement settings")
	}
}

func TestVersionDisplayPlacementIsValidatedAndKeepsLegacyReadersCompatible(t *testing.T) {
	s := newV1Server(t)

	if got := publicSiteSettings(t, s)["versionDisplay"]; got != "footer" {
		t.Fatalf("default versionDisplay=%v, want footer", got)
	}
	rec := postSettingsJSON(t, s, map[string]any{"versionDisplay": "header"})
	if rec.Code != http.StatusOK {
		t.Fatalf("save header placement status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting("version_display", ""); got != "header" {
		t.Fatalf("stored version_display=%q", got)
	}
	if !settingBool(s.st.GetSetting("footer_show_version", ""), false) {
		t.Fatal("the legacy visibility flag must stay true for an old frontend")
	}
	got := publicSiteSettings(t, s)
	if got["versionDisplay"] != "header" || got["footerShowVersion"] != true {
		t.Fatalf("header placement payload=%v", got)
	}

	rec = postSettingsJSON(t, s, map[string]any{"siteTitle": "must not save", "versionDisplay": "somewhere"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid placement status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting("site_title", ""); got != "" {
		t.Fatalf("invalid placement half-applied site title %q", got)
	}

	// An older settings page still maps its boolean onto the new three-state setting.
	rec = postSettingsJSON(t, s, map[string]any{"footerShowVersion": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy hide status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := publicSiteSettings(t, s)["versionDisplay"]; got != "hidden" {
		t.Fatalf("legacy false mapped to %v, want hidden", got)
	}
}

func TestAnnouncementSettingsRejectLongFieldsAtomically(t *testing.T) {
	s := newV1Server(t)
	rec := postSettingsJSON(t, s, map[string]any{
		"announcementEnabled": true,
		"announcementPopup":   true,
		"announcementLevel":   "warning",
		"announcementTitle":   "原公告",
		"announcementContent": "原正文",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("initial save status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = postSettingsJSON(t, s, map[string]any{
		"announcementEnabled": false,
		"announcementTitle":   strings.Repeat("界", maxAnnouncementTitleRunes+1),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long title status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !settingBool(s.st.GetSetting("announcement_enabled", ""), false) ||
		s.st.GetSetting("announcement_title", "") != "原公告" {
		t.Fatalf("long title request half-applied: enabled=%q title=%q",
			s.st.GetSetting("announcement_enabled", ""), s.st.GetSetting("announcement_title", ""))
	}

	rec = postSettingsJSON(t, s, map[string]any{
		"announcementLevel":   "error",
		"announcementContent": strings.Repeat("内", maxAnnouncementContentRunes+1),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long content status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if s.st.GetSetting("announcement_level", "") != "warning" ||
		s.st.GetSetting("announcement_content", "") != "原正文" {
		t.Fatalf("long content request half-applied: level=%q content=%q",
			s.st.GetSetting("announcement_level", ""), s.st.GetSetting("announcement_content", ""))
	}
}

// The level is normalized on the way IN, which is what makes " Warning " and "warning" the same
// stored row. This used to also assert the normalized value came back out of /api/site; it no
// longer does, because the announcement left that payload (ADR 0025). The reader-side half of the
// same rule — that an empty stored level reads as notice — is now TestAnnouncementLevelFallback.
func TestAnnouncementLevelNormalizationIsStored(t *testing.T) {
	s := newV1Server(t)

	rec := postSettingsJSON(t, s, map[string]any{"announcementLevel": " Warning "})
	if rec.Code != http.StatusOK {
		t.Fatalf("save mixed-case level status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting("announcement_level", ""); got != "warning" {
		t.Fatalf("stored level=%q, want warning", got)
	}

	rec = postSettingsJSON(t, s, map[string]any{"announcementLevel": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear level status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := normalizeAnnouncementLevel(s.st.GetSetting("announcement_level", "")); got != "notice" {
		t.Fatalf("empty level should read as notice, got %q", got)
	}
}
