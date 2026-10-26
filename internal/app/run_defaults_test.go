package app

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// The run-form defaults are the only settings in the batch config that name another row (a workflow,
// a preset window). That reference is what these tests are about: it has to survive a round trip,
// be clearable, be refused when it names nothing, and stop being reported once the thing it names
// is gone — a default the run form would ignore must not sit in the admin's picker looking live.

// seedRunPreset creates one enabled daily window and returns its id.
func seedRunPreset(t *testing.T, srv *Server) int64 {
	t.Helper()
	pr := post(t, srv.apiRunPresetCreate,
		`{"label":"低峰期","freq":"daily","intervals":[{"start":{"time":"09:00"},"stop":{"time":"12:00"}}],"on_overrun":"next","enabled":true}`)
	return int64(pr["id"].(float64))
}

func TestRunDefaultsRoundTrip(t *testing.T) {
	srv := batchServer(t)
	targetID := seedTarget(t, srv.st)
	presetID := seedRunPreset(t, srv)

	post(t, srv.apiBatchConfigSave, fmt.Sprintf(
		`{"run_default_target_id":%d,"run_default_preset_id":%d,"run_default_mode":"preset","run_default_idle":true,"run_default_retries":3,"run_default_notify":true}`,
		targetID, presetID))

	cfg := post(t, srv.apiBatchConfigGet, "{}")
	if got := int64(cfg["run_default_target_id"].(float64)); got != targetID {
		t.Errorf("default target = %d, want %d", got, targetID)
	}
	if got := int64(cfg["run_default_preset_id"].(float64)); got != presetID {
		t.Errorf("default preset = %d, want %d", got, presetID)
	}
	if got := cfg["run_default_mode"]; got != "preset" {
		t.Errorf("default mode = %v, want preset", got)
	}
	if cfg["run_default_idle"] != true || cfg["run_default_notify"] != true {
		t.Errorf("idle/notify did not round-trip: %v / %v", cfg["run_default_idle"], cfg["run_default_notify"])
	}
	if got := int(cfg["run_default_retries"].(float64)); got != 3 {
		t.Errorf("default retries = %d, want 3", got)
	}

	// The run forms read the same block off the presets endpoint, under the shorter prefix — one
	// source, two spellings, and they must agree or the dialog opens on something the admin never set.
	ps := post(t, srv.apiRunPresets, "{}")
	if got := int64(ps["default_target_id"].(float64)); got != targetID {
		t.Errorf("presets endpoint default target = %d, want %d", got, targetID)
	}
	if got := int64(ps["default_preset_id"].(float64)); got != presetID {
		t.Errorf("presets endpoint default preset = %d, want %d", got, presetID)
	}
	if got := int(ps["default_retries"].(float64)); got != 3 {
		t.Errorf("presets endpoint default retries = %d, want 3", got)
	}
}

// "No default" is a choice, not an unset field: 0 has to clear a stored default rather than read
// as "leave it alone" (which is what an omitted field means).
func TestRunDefaultsZeroClears(t *testing.T) {
	srv := batchServer(t)
	targetID := seedTarget(t, srv.st)
	post(t, srv.apiBatchConfigSave, fmt.Sprintf(`{"run_default_target_id":%d}`, targetID))

	post(t, srv.apiBatchConfigSave, `{"run_default_target_id":0}`)
	if got := srv.runDefaultTargetID(); got != 0 {
		t.Fatalf("default target after clearing = %d, want 0", got)
	}
	// An omitted field is still untouched — the save stays partial.
	post(t, srv.apiBatchConfigSave, fmt.Sprintf(`{"run_default_target_id":%d}`, targetID))
	post(t, srv.apiBatchConfigSave, `{"run_default_retries":2}`)
	if got := srv.runDefaultTargetID(); got != targetID {
		t.Fatalf("default target was overwritten by an unrelated save: %d", got)
	}
}

// A default naming a workflow or window that does not exist is never stored: the forms would
// ignore it, and storing it would make the admin's Save look like it took.
func TestRunDefaultsRejectUnknownRows(t *testing.T) {
	srv := batchServer(t)
	post(t, srv.apiBatchConfigSave, `{"run_default_target_id":404,"run_default_preset_id":404,"run_default_mode":"whenever","run_default_retries":99}`)

	if got := srv.runDefaultTargetID(); got != 0 {
		t.Errorf("unknown target was stored: %d", got)
	}
	if got := srv.runDefaultPresetID(); got != 0 {
		t.Errorf("unknown preset was stored: %d", got)
	}
	if got := srv.runDefaultMode(); got != "now" {
		t.Errorf("unknown mode was stored: %q", got)
	}
	if got := srv.runDefaultRetries(); got != 0 {
		t.Errorf("out-of-range retries were stored: %d", got)
	}
}

// Deleting the workflow or window a default points at leaves the setting dangling. It must read as
// "no default" from then on, which is what every form will actually do with it.
func TestRunDefaultsForgetDeletedRows(t *testing.T) {
	srv := batchServer(t)
	targetID := seedTarget(t, srv.st)
	presetID := seedRunPreset(t, srv)
	post(t, srv.apiBatchConfigSave, fmt.Sprintf(`{"run_default_target_id":%d,"run_default_preset_id":%d}`, targetID, presetID))

	if err := srv.st.DeleteTarget(targetID); err != nil {
		t.Fatal(err)
	}
	if err := srv.st.DeleteRunPreset(presetID); err != nil {
		t.Fatal(err)
	}
	if got := srv.runDefaultTargetID(); got != 0 {
		t.Errorf("deleted target still reported as the default: %d", got)
	}
	if got := srv.runDefaultPresetID(); got != 0 {
		t.Errorf("deleted preset still reported as the default: %d", got)
	}
}

// A window switched off is a different case from a deleted one: the run form won't offer it, but
// the admin who turned it off should find their choice waiting rather than silently cleared.
func TestRunDefaultsKeepDisabledPreset(t *testing.T) {
	srv := batchServer(t)
	presetID := seedRunPreset(t, srv)
	post(t, srv.apiBatchConfigSave, fmt.Sprintf(`{"run_default_preset_id":%d}`, presetID))

	p, ok := srv.st.GetRunPreset(presetID)
	if !ok {
		t.Fatal("preset vanished")
	}
	p.Enabled = false
	if err := srv.st.UpdateRunPreset(p); err != nil {
		t.Fatal(err)
	}
	if got := srv.runDefaultPresetID(); got != presetID {
		t.Fatalf("disabled preset was dropped as the default: %d, want %d", got, presetID)
	}
}

// Whether the run form prints a window's whole rule next to its name is an admin display choice,
// not a default the user overrides — so it has its own key on both endpoints. Both spellings come
// off the same getter, and an unconfigured portal reads "off": the picker says which window is
// chosen and leaves the rule to the info button beside it.
func TestRunShowPresetRuleRoundTrip(t *testing.T) {
	srv := batchServer(t)

	cfg := post(t, srv.apiBatchConfigGet, "{}")
	if cfg["run_show_preset_rule"] != false {
		t.Errorf("unconfigured show-rule = %v, want false", cfg["run_show_preset_rule"])
	}
	if ps := post(t, srv.apiRunPresets, "{}"); ps["show_preset_rule"] != false {
		t.Errorf("unconfigured show-rule on the presets endpoint = %v, want false", ps["show_preset_rule"])
	}

	post(t, srv.apiBatchConfigSave, `{"run_show_preset_rule":true}`)
	if cfg := post(t, srv.apiBatchConfigGet, "{}"); cfg["run_show_preset_rule"] != true {
		t.Errorf("saved show-rule = %v, want true", cfg["run_show_preset_rule"])
	}
	if ps := post(t, srv.apiRunPresets, "{}"); ps["show_preset_rule"] != true {
		t.Errorf("show-rule on the presets endpoint = %v, want true", ps["show_preset_rule"])
	}

	// An omitted field leaves the setting alone (the batch config save is partial); an explicit
	// false clears it, the same shape as the rest of the block.
	post(t, srv.apiBatchConfigSave, `{"max_jobs":4}`)
	if cfg := post(t, srv.apiBatchConfigGet, "{}"); cfg["run_show_preset_rule"] != true {
		t.Errorf("an unrelated save cleared show-rule: %v", cfg["run_show_preset_rule"])
	}
	post(t, srv.apiBatchConfigSave, `{"run_show_preset_rule":false}`)
	if cfg := post(t, srv.apiBatchConfigGet, "{}"); cfg["run_show_preset_rule"] != false {
		t.Errorf("show-rule did not turn back off: %v", cfg["run_show_preset_rule"])
	}
}

func TestRunDefaultsOUInheritanceAndExplicitZero(t *testing.T) {
	s := batchServer(t)
	root := s.st.EnsureDefaultGroup()
	parent, _ := s.st.CreateUserGroup("Operations", "", 0)
	child, _ := s.st.CreateUserGroup("Research", "", 0)
	s.st.SetGroupParent(parent, root)
	s.st.SetGroupParent(child, parent)
	s.setRunDefaultRetries(4)
	if err := s.st.SetSetting(runDefaultsOUKey(parent), `{"run_default_retries":2,"run_default_idle":true}`); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(runDefaultsOUKey(child), `{"run_default_retries":0,"run_default_idle":false}`); err != nil {
		t.Fatal(err)
	}
	snap, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil {
		t.Fatal(err)
	}
	out, inherited, sources, _, err := s.resolveRunDefaultsOU(snap.graph, child)
	if err != nil {
		t.Fatal(err)
	}
	if out["run_default_retries"] != float64(0) || out["run_default_idle"] != false || inherited["run_default_retries"] != float64(2) || sources["run_default_retries"] != "Operations" {
		t.Fatalf("resolution: %+v %+v %+v", out, inherited, sources)
	}
	s.st.SetSetting(runDefaultsOUKey(child), `{"run_default_retries":null}`)
	out, _, _, _, err = s.resolveRunDefaultsOU(snap.graph, child)
	if err != nil || out["run_default_retries"] != float64(2) || out["run_default_idle"] != true {
		t.Fatalf("inheritance: %+v %v", out, err)
	}
}

func TestRunDefaultsOUValidationAndCleanup(t *testing.T) {
	s := batchServer(t)
	id, err := s.st.CreateUserGroup("Research", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	request := func(body string, want int) {
		t.Helper()
		req := httptest.NewRequest("PUT", "/x", strings.NewReader(body))
		req.SetPathValue("id", fmt.Sprint(id))
		rec := httptest.NewRecorder()
		s.apiRunDefaultsOU(rec, req, "admin")
		if rec.Code != want {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
		}
	}
	request(`{"run_default_retries":3,"run_default_idle":false}`, 200)
	saved := s.st.GetSetting(runDefaultsOUKey(id), "")
	for _, body := range []string{`{"run_default_retries":6}`, `{"run_default_retries":1.5}`, `{"run_default_mode":"other"}`, `{"run_default_idle":"true"}`, `{"run_default_target_id":99999}`, `{"unknown":null}`, `null`} {
		request(body, 400)
		if got := s.st.GetSetting(runDefaultsOUKey(id), ""); got != saved {
			t.Fatalf("invalid request changed defaults: %s", got)
		}
	}
	if _, _, err := s.st.DeleteUserGroup(id); err != nil {
		t.Fatal(err)
	}
	if got := s.st.GetSetting(runDefaultsOUKey(id), "missing"); got != "missing" {
		t.Fatalf("deleted OU retained defaults: %s", got)
	}
	request(`{}`, 404)
}

func TestRunPresetsUsesMemberOUDefaults(t *testing.T) {
	s := batchServer(t)
	root := s.st.EnsureDefaultGroup()
	id, err := s.st.CreateUserGroup("Research", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetGroupParent(id, root); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetPrimaryGroup("admin", id); err != nil {
		t.Fatal(err)
	}
	s.setRunDefaultRetries(4)
	if err := s.st.SetSetting(runDefaultsOUKey(root), `{"run_default_retries":2}`); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(runDefaultsOUKey(id), `{"run_default_idle":true,"run_show_preset_rule":true}`); err != nil {
		t.Fatal(err)
	}
	got := post(t, s.apiRunPresets, `{}`)
	if got["default_retries"] != float64(2) || got["default_idle"] != true || got["show_preset_rule"] != true {
		t.Fatalf("member defaults=%+v", got)
	}
	if s.runDefaultRetries() != 4 {
		t.Fatal("OU defaults changed global configuration")
	}
}
