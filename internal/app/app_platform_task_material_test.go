package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// An unauthenticated caller must not make the Portal do database work. A credential that names a real
// installation but carries a forged MAC is refused from verification material the Portal keeps in memory
// after the first full verification; without that material the full verification runs under a small
// global limit that never waits.

// forgeIdentity returns a well-formed, canonical credential for the same installation whose MAC is wrong.
func forgeIdentity(t *testing.T, identity []byte, n int) []byte {
	t.Helper()
	var grant appplatform.ServiceIdentityGrant
	if err := json.Unmarshal(identity, &grant); err != nil {
		t.Fatal(err)
	}
	grant.LeaseID = fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	forged, err := json.Marshal(grant) // the MAC is the real one for another lease: it cannot match
	if err != nil {
		t.Fatal(err)
	}
	return forged
}

func (f *taskRefusalFixture) forgedSubmit(t *testing.T, n int) int {
	t.Helper()
	body := `{"occurrenceId":"forged","handler":"app.backend.task","payload":{"v":1}}`
	return platformTaskHTTPWithIdentity(t, f.s, f.installation, body, forgeIdentity(t, f.identity, n)).Code
}

func TestPlatformServiceTaskForgedIdentityFloodTouchesNoDatabaseOnceTheMaterialIsWarm(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	if code, body := f.submit(t, "warm"); code != 200 {
		t.Fatalf("warm-up: %d %s", code, body)
	}
	var codes sync.Map
	flood := func() {
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < 25; i++ {
					codes.Store(f.forgedSubmit(t, w*100+i+1), true)
				}
			}(w)
		}
		wg.Wait()
	}
	statements := f.statements(flood)
	if len(statements) != 0 {
		t.Fatalf("a flood of forged credentials issued %d statements: %v", len(statements), statements[:min(len(statements), 5)])
	}
	codes.Range(func(k, _ any) bool {
		if k.(int) != 404 {
			t.Errorf("a forged credential was answered %d", k)
		}
		return true
	})
	// And while it runs a legitimate backend keeps succeeding.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
				f.forgedSubmit(t, 100000+n)
			}
		}
	}()
	for i := 0; i < 5; i++ {
		if code, body := f.submit(t, fmt.Sprintf("during-flood-%d", i)); code != 200 {
			t.Errorf("legitimate submission %d answered %d %s during a forged flood", i, code, body)
		}
	}
	close(stop)
	<-done
}

func TestPlatformServiceTaskColdVerificationIsLimitedAndNeverWaits(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	f.s.platformServiceMaterial.reset()
	var held []func()
	for i := 0; i < platformUnverifiedVerifications; i++ {
		release, ok := f.s.platformUnverified.acquire()
		if !ok {
			t.Fatalf("unverified slot %d refused", i)
		}
		held = append(held, release)
	}
	var code int
	statements := f.statements(func() { code = f.forgedSubmit(t, 1) })
	if code != 429 || len(statements) != 0 {
		t.Fatalf("cold forged credential with the limit full: %d with %d statements", code, len(statements))
	}
	if code, _ := f.submit(t, "cold-legitimate"); code != 429 {
		t.Fatalf("a cold legitimate credential with the limit full was answered %d, want a retryable 429", code)
	}
	for _, release := range held {
		release()
	}
	if code := f.forgedSubmit(t, 2); code != 404 {
		t.Fatalf("cold forged credential with the limit free: %d", code)
	}
	if code, body := f.submit(t, "cold-legitimate-2"); code != 200 {
		t.Fatalf("cold legitimate credential with the limit free: %d %s", code, body)
	}
	if statements := f.statements(func() { f.forgedSubmit(t, 3) }); len(statements) != 0 {
		t.Fatalf("a successful verification did not warm the material: %v", statements)
	}
}

func TestPlatformServiceTaskMalformedCredentialsNeverTouchTheDatabase(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	f.s.platformServiceMaterial.reset()
	for name, raw := range map[string][]byte{
		"not json":              []byte(`not json`),
		"no installation":       []byte(`{"format":"x"}`),
		"another installation":  []byte(`{"installation_id":"11111111-1111-4111-8111-111111111111"}`),
		"installation not uuid": []byte(`{"installation_id":"nope"}`),
	} {
		var code int
		statements := f.statements(func() {
			code = platformTaskHTTPWithIdentity(t, f.s, f.installation, `{"occurrenceId":"m","handler":"app.backend.task","payload":{}}`, raw).Code
		})
		if code != 404 || len(statements) != 0 {
			t.Errorf("%s: %d with %d statements", name, code, len(statements))
		}
	}
}

// Material is only ever a reason to refuse: an authentic credential of an installation that is no longer
// enabled still goes through the live verification and is refused there. The material is kept: its MAC check
// was just proved right, and the refresh drops the material of an installation that is no longer ready.
func TestPlatformServiceTaskMaterialNeverAcceptsAndIsKeptWhenTheLiveVerificationRefuses(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	f.submit(t, "warm")
	if f.s.platformServiceMaterial.size() != 1 {
		t.Fatalf("material not warm: %d", f.s.platformServiceMaterial.size())
	}
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, f.installation)
	if code, _ := f.submit(t, "after-disable"); code != 404 {
		t.Fatalf("a disabled installation's credential answered %d", code)
	}
	if f.s.platformServiceMaterial.size() != 1 {
		t.Fatal("a live refusal of an authentic credential dropped material whose MAC check was right")
	}
	if err := f.s.refreshPlatformServiceMaterial(); err != nil {
		t.Fatal(err)
	}
	if f.s.platformServiceMaterial.size() != 0 {
		t.Fatal("the refresh kept the material of a retired installation")
	}
}

// A holder of an authentic credential the live verification refuses (a stale generation, a retired lease)
// must not be able to evict the material and so push the installation's real backend into the Portal-wide
// cold-verification limit: with that limit full, the refused credential keeps being answered 404 from its own
// slots, and a forged one still never reaches the database.
func TestPlatformServiceTaskALiveRefusalCannotPushTheInstallationIntoColdVerification(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	if code, body := f.submit(t, "warm"); code != 200 {
		t.Fatalf("warm-up: %d %s", code, body)
	}
	var held []func()
	for i := 0; i < platformUnverifiedVerifications; i++ {
		release, ok := f.s.platformUnverified.acquire()
		if !ok {
			t.Fatal("slot refused")
		}
		held = append(held, release)
	}
	defer func() {
		for _, release := range held {
			release()
		}
	}()
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, f.installation)
	for i := 0; i < 5; i++ {
		if code, body := f.submit(t, fmt.Sprintf("refused-live-%d", i)); code != 404 {
			t.Fatalf("refused credential %d answered %d %s, want 404 from the installation's own slots", i, code, body)
		}
	}
	if statements := f.statements(func() { f.forgedSubmit(t, 1) }); len(statements) != 0 {
		t.Fatalf("after live refusals a forged credential reached the database: %v", statements)
	}
}

// An expired credential whose MAC is right is refused from memory, before it takes a slot or a statement. The
// expiry is judged on the database's clock, as the live verification judges it, through the offset the refresh
// learns.
func TestPlatformServiceTaskExpiredCredentialIsRefusedFromMemory(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	if code, body := f.submit(t, "warm"); code != 200 {
		t.Fatalf("warm-up: %d %s", code, body)
	}
	var grant appplatform.ServiceIdentityGrant
	if err := json.Unmarshal(f.identity, &grant); err != nil {
		t.Fatal(err)
	}
	expires, err := strconv.ParseInt(grant.ExpiresMS, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	forgedBefore, expiredBefore := f.s.platformServiceMaterial.refusals()
	f.s.platformServiceMaterial.databaseOffsetMS.Store(expires - time.Now().UnixMilli())
	var code int
	statements := f.statements(func() { code, _ = f.submit(t, "expired") })
	if code != 404 || len(statements) != 0 {
		t.Fatalf("an expired credential: %d with %d statements %v", code, len(statements), statements)
	}
	if f.forgedSubmit(t, 1) != 404 {
		t.Fatal("a forged credential was not refused")
	}
	forged, expired := f.s.platformServiceMaterial.refusals()
	if forged-forgedBefore != 1 || expired-expiredBefore != 1 {
		t.Fatalf("refusals counted forged %d expired %d, want 1 and 1", forged-forgedBefore, expired-expiredBefore)
	}
	if f.s.platformServiceMaterial.size() != 1 {
		t.Fatal("an expired credential dropped the material")
	}
	f.s.platformServiceMaterial.databaseOffsetMS.Store(0)
	if code, body := f.submit(t, "live-again"); code != 200 {
		t.Fatalf("the credential before its expiry: %d %s", code, body)
	}
}

// On PostgreSQL the refresh learns how far the database's clock is from the Portal's, so the in-memory expiry
// check agrees with the live verification even when the two clocks differ.
func TestPlatformServiceMaterialRefreshLearnsThePostgresClockOffset(t *testing.T) {
	s := &Server{st: pgStore(t)}
	s.platformServiceMaterial.databaseOffsetMS.Store(1 << 40)
	if err := s.refreshPlatformServiceMaterial(); err != nil {
		t.Fatal(err)
	}
	if offset := s.platformServiceMaterial.databaseOffsetMS.Load(); offset > 5_000 || offset < -5_000 {
		t.Fatalf("offset %d ms between two clocks of one host", offset)
	}
	// A Portal clock an hour behind is an offset of an hour.
	s.platformServiceMaterial.now = func() time.Time { return time.Now().Add(-time.Hour) }
	if err := s.refreshPlatformServiceMaterial(); err != nil {
		t.Fatal(err)
	}
	if offset := s.platformServiceMaterial.databaseOffsetMS.Load(); offset < 3_595_000 || offset > 3_605_000 {
		t.Fatalf("offset %d ms with the Portal an hour behind", offset)
	}
}

// A clock reading is used only when its round trip was short: a reading that waited for a pool connection would
// put the database's clock late by half the wait, and the in-memory check would then refuse a live credential
// before its expiry.
func TestPlatformClockOffsetKeepsOnlyAShortReading(t *testing.T) {
	before := time.UnixMilli(1_000_000)
	if offset, ok := platformClockOffset(before, 1_000_510, before.Add(20*time.Millisecond)); !ok || offset != 500 {
		t.Fatalf("a short reading: %d %v, want 500 true", offset, ok)
	}
	if _, ok := platformClockOffset(before, 1_000_510, before.Add(platformClockReadingMax+time.Millisecond)); ok {
		t.Fatal("a reading that waited longer than the bound was used")
	}
	if _, ok := platformClockOffset(before, 1_000_510, before.Add(-time.Millisecond)); ok {
		t.Fatal("a reading whose Portal clock ran backwards was used")
	}
}

// On SQLite the database's clock is the Portal's own, so the offset is zero whatever a reading says.
func TestPlatformServiceMaterialRefreshOnSQLiteKeepsNoOffset(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	if f.s.st.driver != "sqlite" {
		t.Skip("SQLite only")
	}
	f.s.platformServiceMaterial.databaseOffsetMS.Store(1234)
	f.s.platformServiceMaterial.now = func() time.Time { return time.Now().Add(-time.Hour) }
	if err := f.s.refreshPlatformServiceMaterial(); err != nil {
		t.Fatal(err)
	}
	if offset := f.s.platformServiceMaterial.databaseOffsetMS.Load(); offset != 0 {
		t.Fatalf("offset %d on SQLite", offset)
	}
}

// The Portal-wide limit on cold verification has the size its constant names.
func TestPlatformUnverifiedLimitIsItsConstant(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	var held []func()
	for {
		release, ok := f.s.platformUnverified.acquire()
		if !ok {
			break
		}
		held = append(held, release)
		if len(held) > 64 {
			break
		}
	}
	for _, release := range held {
		release()
	}
	if len(held) != platformUnverifiedVerifications {
		t.Fatalf("%d cold verifications admitted at once, the constant is %d", len(held), platformUnverifiedVerifications)
	}
}

func TestPlatformServiceMaterialExpiresIsBoundedAndIsZeroedOnEviction(t *testing.T) {
	var c platformServiceMaterialCache
	now := time.Unix(1_000, 0)
	c.now = func() time.Time { return now }
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	if platformServiceMaterialTTL > 30*time.Second {
		t.Fatalf("material lives %s", platformServiceMaterialTTL)
	}
	c.remember("a", seed)
	stored := c.entries["a"]
	if stored == nil {
		t.Fatal("not stored")
	}
	if _, known := c.material("a"); !known {
		t.Fatal("entry missing")
	}
	c.forget("a")
	for _, b := range stored.seed {
		if b != 0 {
			t.Fatal("the seed was not zeroed on eviction")
		}
	}
	c.remember("b", seed)
	stored = c.entries["b"]
	now = now.Add(platformServiceMaterialTTL + time.Millisecond)
	if _, known := c.material("b"); known {
		t.Fatal("entry outlived its time limit")
	}
	for _, b := range stored.seed {
		if b != 0 {
			t.Fatal("the seed was not zeroed on expiry")
		}
	}
	for i := 0; i < platformServiceMaterialMax*3; i++ {
		c.remember(fmt.Sprint("i", i), seed)
	}
	if n := c.size(); n > platformServiceMaterialMax {
		t.Fatalf("cache grew to %d", n)
	}
}

// A tenant at its quota with many parallel connections holds at most its own slots, and with them full
// the remembered refusal answers busy without a statement.
func TestPlatformServiceTaskRememberedRefusalTakesTheInstallationsOwnSlot(t *testing.T) {
	f := newTaskRefusalFixture(t, true)
	if code, _ := f.submit(t, "prime"); code != 429 {
		t.Fatalf("not refused: %d", code)
	}
	var releases []func()
	for i := 0; i < platformServiceTaskSlots; i++ {
		release, ok := f.s.platformServiceTasks.acquire(f.installation)
		if !ok {
			t.Fatal("slot refused")
		}
		releases = append(releases, release)
	}
	var code int
	var body string
	statements := f.statements(func() { code, body = f.submit(t, "slots-full") })
	if code != 429 || !strings.Contains(body, "app_platform_busy") || len(statements) != 0 {
		t.Fatalf("slots full: %d %s with %d statements", code, body, len(statements))
	}
	for _, release := range releases {
		release()
	}
	var inside, widest atomic.Int64
	previousHook := platformTaskSlotHook
	platformTaskSlotHook = func() {
		n := inside.Add(1)
		for {
			w := widest.Load()
			if n <= w || widest.CompareAndSwap(w, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		inside.Add(-1)
	}
	t.Cleanup(func() { platformTaskSlotHook = previousHook })
	var wg sync.WaitGroup
	var busy, refused atomic.Int64
	for w := 0; w < 40; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			code, body := f.submit(t, fmt.Sprintf("parallel-%d", w))
			switch {
			case code == 429 && strings.Contains(body, "app_platform_busy"):
				busy.Add(1)
			case code == 429:
				refused.Add(1)
			default:
				t.Errorf("answered %d %s", code, body)
			}
		}(w)
	}
	wg.Wait()
	if widest.Load() > int64(platformServiceTaskSlots) {
		t.Fatalf("%d refused submissions of one tenant ran at once, the limit is %d", widest.Load(), platformServiceTaskSlots)
	}
	if refused.Load() == 0 {
		t.Fatal("no submission was refused for the quota")
	}
}

// A backend must never be answered busy because of an anonymous flood. Material is therefore kept warm for
// every enabled, ready installation by the Portal itself, not only after a backend's own first success: a
// flood that fills the cold-verification limit then delays nothing a real installation sends.
func TestPlatformServiceMaterialRefreshWarmsEveryReadyInstallationAndSpareTheBackendFromAColdFlood(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	f.s.platformServiceMaterial.reset()
	if err := f.s.refreshPlatformServiceMaterial(); err != nil {
		t.Fatal(err)
	}
	if f.s.platformServiceMaterial.size() != 1 {
		t.Fatalf("%d installations warm, want 1", f.s.platformServiceMaterial.size())
	}
	var held []func()
	for i := 0; i < platformUnverifiedVerifications; i++ {
		release, ok := f.s.platformUnverified.acquire()
		if !ok {
			t.Fatal("slot refused")
		}
		held = append(held, release)
	}
	defer func() {
		for _, release := range held {
			release()
		}
	}()
	if code, body := f.submit(t, "during-a-cold-flood"); code != 200 {
		t.Fatalf("a legitimate backend was answered %d %s while the cold-verification limit was full", code, body)
	}
	if statements := f.statements(func() { f.forgedSubmit(t, 1) }); len(statements) != 0 {
		t.Fatalf("a forged credential reached the database: %v", statements)
	}
}

func TestPlatformServiceMaterialRefreshSkipsRetiredInstallationsAndDropsTheirMaterial(t *testing.T) {
	f := newTaskRefusalFixture(t, false)
	f.s.platformServiceMaterial.reset()
	f.s.refreshPlatformServiceMaterial()
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, f.installation)
	if err := f.s.refreshPlatformServiceMaterial(); err != nil {
		t.Fatal(err)
	}
	if f.s.platformServiceMaterial.size() != 0 {
		t.Fatal("the material of a disabled installation was kept")
	}
}
