package journal

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const daemonDetailMarker = "daemon detail: "

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	previous, flags := log.Writer(), log.Flags()
	log.SetOutput(&out)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previous); log.SetFlags(flags) })
	return &out
}

// The daemon's own error text for a rejected load is for the operator's log only: printable ASCII, bounded to 512
// bytes, on one line. It reaches no receipt, row or reply.
func TestLoadImageLogsTheBoundedSanitizedDaemonDetailAndKeepsItOutOfTheReceipt(t *testing.T) {
	f := newLoadFixture(t)
	f.uploadAll(t)
	secret := "status 500: bad layout\n\x1b[31mé\r\x00" + strings.Repeat("x", 2000)
	f.native.snapshot = func(r driver.LoadImageRequest, f loadFixtureT) driver.LoadImageSnapshot {
		return driver.LoadImageSnapshot{ReleaseSHA256: r.ReleaseSHA256, Platform: r.Platform, ImageID: driver.ExpectedImageID(driver.ImageStoreClassic, f.entry),
			Store: driver.ImageStoreClassic, State: driver.LoadStateRejected, Outcome: driver.LoadOutcomeLoadRejected, Removed: true,
			ObservedMS: time.Now().UnixMilli(), DaemonDetail: secret}
	}
	out := captureLog(t)
	got, err := f.dispatch()
	receipt, ok := got.(LoadImageReceipt)
	if err != nil || !ok || receipt.Outcome != driver.LoadOutcomeLoadRejected {
		t.Fatalf("rejection: %+v %v", got, err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	var detail string
	for _, line := range lines {
		if _, rest, found := strings.Cut(line, daemonDetailMarker); found {
			if detail != "" {
				t.Fatal("the detail was logged twice")
			}
			detail = rest
		}
	}
	if !strings.HasPrefix(detail, "status 500: bad layout") {
		t.Fatalf("the daemon's text is not in the log: %q", out.String())
	}
	if len(detail) > 512 {
		t.Fatalf("the logged detail is %d bytes, over the 512 bound", len(detail))
	}
	for i := 0; i < len(detail); i++ {
		if detail[i] < 0x20 || detail[i] > 0x7e {
			t.Fatalf("byte %#x at %d is not printable ASCII", detail[i], i)
		}
	}
	// Never in a receipt, a journal row or a reply.
	reply, _ := json.Marshal(got)
	var row []byte
	if err := f.store.db.QueryRow(`SELECT COALESCE(receipt_json, '') FROM runner_commands WHERE effect_id=?`, f.command.EffectID).Scan(&row); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"reply": reply, "journal row": row} {
		if bytes.Contains(body, []byte("bad layout")) || bytes.Contains(body, []byte("status 500")) {
			t.Fatalf("the daemon detail leaked into the %s: %s", name, body)
		}
	}
}

func TestLoadImageLogsNothingWhenThereIsNoDaemonDetail(t *testing.T) {
	f := newLoadFixture(t)
	f.uploadAll(t)
	out := captureLog(t)
	if _, err := f.dispatch(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), daemonDetailMarker) {
		t.Fatalf("an empty detail was logged: %q", out.String())
	}
}

func TestPrintableDaemonDetailIsBoundedAndPrintableASCII(t *testing.T) {
	if got := printableDaemonDetail("ok: fine"); got != "ok: fine" {
		t.Fatalf("plain text changed: %q", got)
	}
	got := printableDaemonDetail("a\nb\tcé\x7f" + strings.Repeat("z", 1000))
	if len(got) != 512 || !strings.HasPrefix(got, "a?b?c??") {
		t.Fatalf("bounded or sanitized wrongly: %d %q", len(got), got[:12])
	}
}
