//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func declaredImageBytes(f *loadFixtureT) int64 {
	var total int64
	for _, l := range f.entry.Layers {
		total += int64(l.UncompressedBytes)
	}
	return total
}

func TestLoadImageReservesTheDeclaredBytesOnce(t *testing.T) {
	f := newLoadFixture(t)
	total := declaredImageBytes(f)
	budgets := roomyBudgets()
	budgets.ImageStoreBytes = total // room for exactly one copy
	if err := f.store.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	f.uploadAll(t)
	if _, err := f.dispatch(); err != nil {
		t.Fatal(err)
	}
	resource := imageResourceID(f.entry.ConfigDigest)
	var kind, state string
	var image int64
	if err := f.store.db.QueryRow(`SELECT kind,state,image_bytes FROM runner_reservations WHERE resource_id=?`, resource).Scan(&kind, &state, &image); err != nil {
		t.Fatalf("no image reservation: %v", err)
	}
	if kind != "image" || state != "held" || image != total {
		t.Fatalf("reservation = %s %s %d (want %d)", kind, state, image, total)
	}
	// A second installation loads the very same image: it is counted once, so it still fits.
	second := f.buildCommand(t, 2100)
	f.command = second
	f.raw, f.sig = f.sign(t, second)
	f.uploadAll(t)
	if _, err := f.dispatch(); err != nil {
		t.Fatalf("the same config digest was counted twice: %v", err)
	}
	if scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE kind='image'`) != 1 {
		t.Fatal("a second reservation was made for one config digest")
	}
}

func TestLoadImageRefusedRunsNothingAndReplays(t *testing.T) {
	f := newLoadFixture(t)
	total := declaredImageBytes(f)
	budgets := roomyBudgets()
	budgets.ImageStoreBytes = total - 1
	if err := f.store.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	f.uploadAll(t)
	_, err := f.dispatch()
	var refusal *CapacityRefusalError
	if !errors.As(err, &refusal) || refusal.Receipt.Dimension != CapacityImageStore {
		t.Fatalf("err = %v", err)
	}
	if refusal.Receipt.Remaining != itoa(total-1) {
		t.Fatalf("remaining = %s", refusal.Receipt.Remaining)
	}
	if f.native.calls != 0 {
		t.Fatal("a refused load reached the daemon")
	}
	if scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_reservations`) != 0 {
		t.Fatal("a refused load left a reservation")
	}
	if state, _ := f.rowState(t); state != "reverted" {
		t.Fatalf("row state = %q", state)
	}
	_, err = f.dispatch()
	var again *CapacityRefusalError
	if !errors.As(err, &again) || again.Receipt != refusal.Receipt {
		t.Fatalf("replay = %v", err)
	}
	got, err := f.store.ReadLoadImageOrStatus(context.Background(), f.raw, f.readSig(t, f.raw), f.descriptor, f.native, f.scratch)
	if receipt, ok := got.(CapacityRefusal); err != nil || !ok || receipt != refusal.Receipt {
		t.Fatalf("read-back = %#v %v", got, err)
	}
	if f.native.calls != 0 {
		t.Fatal("a replay reached the daemon")
	}
}

func TestRejectedLoadReleasesItsReservationUnlessAnotherLoadHoldsTheImage(t *testing.T) {
	f := newLoadFixture(t)
	if err := f.store.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	f.native.snapshot = func(r driver.LoadImageRequest, fx loadFixtureT) driver.LoadImageSnapshot {
		return driver.LoadImageSnapshot{ReleaseSHA256: r.ReleaseSHA256, Platform: r.Platform, ImageID: driver.ExpectedImageID(driver.ImageStoreClassic, fx.entry),
			Store: driver.ImageStoreClassic, State: driver.LoadStateRejected, Outcome: driver.LoadOutcomeLoadRejected, ObservedMS: 1, Removed: true}
	}
	f.uploadAll(t)
	if _, err := f.dispatch(); err != nil {
		t.Fatal(err)
	}
	if state, _ := f.rowState(t); state != "reverted" {
		t.Fatalf("row state = %q", state)
	}
	if scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE kind='image' AND state='released'`) != 1 {
		t.Fatal("a rejected load kept its reservation")
	}
}
