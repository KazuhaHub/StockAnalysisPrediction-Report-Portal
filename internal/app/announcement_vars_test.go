package app

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The grammar is shared with web/src/lib/announcementVars.ts; the cases below are mirrored in
// announcementVars.test.ts so the editor and the server accept exactly the same text.
func TestCheckAnnouncementVars(t *testing.T) {
	ok := []string{
		"",
		"plain text with no variables",
		"a lone {{ without a close",
		"{{now}}",
		"{{ now }}",
		"{{now|date}}",
		"{{now|time}}",
		"{{now|datetime}}",
		"{{now|Asia/Tokyo}}",
		"{{now|time|America/New_York}}",
		"{{now|UTC}}",
		"{{time:2026-11-20T14:00:00Z}}",
		"{{time:2026-11-20T14:00Z}}",
		"{{time:2026-11-20T22:00:00+08:00}}",
		"{{time:2026-11-20T14:00:00.500Z}}",
		"{{time:2026-11-20T14:00:00Z|date}}",
		"{{time:2026-11-20T14:00:00Z|Asia/Shanghai}}",
		"{{time:2026-11-20T14:00:00Z|Asia/Shanghai|time}}",
		"{{time:2026-11-20T14:00:00Z|America/Argentina/Buenos_Aires}}",
		"{{countdown:2026-11-20T14:00:00Z}}",
		"{{site}}",
		"{{time:2026-11-20T14:00+14:00}}",
		"{{time:2026-11-20T14:00-12:00}}",
		"{{time:2028-02-29T00:00Z}}",
		"Maintenance {{time:2026-11-20T14:00Z}} ({{countdown:2026-11-20T14:00Z}}) on {{site}}",
	}
	for _, s := range ok {
		if bad := checkAnnouncementVars(s); bad != "" {
			t.Errorf("checkAnnouncementVars(%q) refused %q, want accepted", s, bad)
		}
	}

	refused := map[string]string{
		"{{}}":                       "{{}}",
		"{{user}}":                   "{{user}}",
		"{{tiem:2026-11-20T14:00Z}}": "{{tiem:2026-11-20T14:00Z}}",
		"{{NOW}}":                    "{{NOW}}",
		"{{now:2026-11-20T14:00Z}}":  "{{now:2026-11-20T14:00Z}}",
		"{{site|date}}":              "{{site|date}}",
		"{{site:x}}":                 "{{site:x}}",
		"{{time}}":                   "{{time}}",
		"{{time:}}":                  "{{time:}}",
		"{{time:2026-11-20}}":        "{{time:2026-11-20}}",
		"{{time:2026-11-20T14:00}}":  "{{time:2026-11-20T14:00}}",
		"{{time:2026-11-20 14:00Z}}": "{{time:2026-11-20 14:00Z}}",
		"{{time:2026-13-40T14:00Z}}": "{{time:2026-13-40T14:00Z}}",
		"{{time:tomorrow}}":          "{{time:tomorrow}}",
		"{{time:2026-11-20T14:00Z|Mars/Olympus}}":   "{{time:2026-11-20T14:00Z|Mars/Olympus}}",
		"{{time:2026-11-20T14:00Z|asia/shanghai}}":  "{{time:2026-11-20T14:00Z|asia/shanghai}}",
		"{{time:2026-11-20T14:00Z|Local}}":          "{{time:2026-11-20T14:00Z|Local}}",
		"{{time:2026-11-20T14:00Z|CST}}":            "{{time:2026-11-20T14:00Z|CST}}",
		"{{time:2026-11-20T14:00Z|date|time}}":      "{{time:2026-11-20T14:00Z|date|time}}",
		"{{time:2026-11-20T14:00Z|UTC|Asia/Tokyo}}": "{{time:2026-11-20T14:00Z|UTC|Asia/Tokyo}}",
		"{{time:2026-11-20T14:00Z|}}":               "{{time:2026-11-20T14:00Z|}}",
		"{{countdown:2026-11-20T14:00Z|date}}":      "{{countdown:2026-11-20T14:00Z|date}}",
		"{{countdown}}":                             "{{countdown}}",
		"{{time:2026-11-20T14:00+24:00}}":           "{{time:2026-11-20T14:00+24:00}}",
		"{{time:2026-11-20T14:00+12:60}}":           "{{time:2026-11-20T14:00+12:60}}",
		"{{time:0099-01-01T00:00Z}}":                "{{time:0099-01-01T00:00Z}}",
		"{{time:2027-02-29T00:00Z}}":                "{{time:2027-02-29T00:00Z}}",
		"{{site\u0085}}":                            "{{site\u0085}}",
		"{{site\ufeff}}":                            "{{site\ufeff}}",
		"ok {{now}} then {{bogus}}":                 "{{bogus}}",
	}
	for s, want := range refused {
		if got := checkAnnouncementVars(s); got != want {
			t.Errorf("checkAnnouncementVars(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestAnnouncementSaveRefusesBadVariable(t *testing.T) {
	s := newV1Server(t)
	for _, body := range []string{
		`{"title":"t","content":"down at {{time:tomorrow}}"}`,
		`{"title":"{{bogus}}","content":"c"}`,
	} {
		rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_announcement_variable") {
			t.Fatalf("POST %s = %d %s, want 400 bad_announcement_variable", body, rec.Code, rec.Body.String())
		}
	}
	id := mustAddAnnouncement(t, s, `{"title":"t","content":"c"}`)
	rec := announcementReq(t, s, http.MethodPut, fmt.Sprintf("/api/admin/announcements/%d", id),
		`{"content":"{{countdown:soon}}"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_announcement_variable") {
		t.Fatalf("PUT with a bad variable = %d %s, want 400 bad_announcement_variable", rec.Code, rec.Body.String())
	}

	// The reader feed carries the template verbatim: rendering happens in the reader's browser,
	// because only the browser knows the reader's zone and clock.
	tmpl := "{{time:2026-11-20T14:00Z|Asia/Shanghai}} on {{site}}"
	mustAddAnnouncement(t, s, `{"title":"Maintenance","content":"`+tmpl+`"}`)
	found := false
	for _, it := range readerFeed(t, s, "admin") {
		if it["content"] == tmpl {
			found = true
		}
	}
	if !found {
		t.Fatalf("reader feed does not carry the template verbatim")
	}
}
