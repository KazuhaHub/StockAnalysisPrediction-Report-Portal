package app

import (
	"testing"
	"time"
)

// The shared prediction fixture must mean the same thing on every day the suite runs.
//
// horizon_std is decided by how far horizon_end lies from the receipt, within +-7 days of twelve
// months, and an unpinned ingest uses the real clock. A hard-coded horizon therefore dated the
// fixture: it read as a standard-horizon prediction only during the week around its own date, and
// afterwards the accepted set stopped receiving its h12 mark — which broke every assertion about
// "one mark per checkpoint" for a reason that had nothing to do with the code under test.
func TestPredWireIsAStandardHorizonPayloadOnAnyDay(t *testing.T) {
	s := newV1Server(t)
	code, m := ingestPost(t, s, ingestBody(string(predWire(nil))))
	if code != 200 || m["predictions_status"] != predStateAccepted {
		t.Fatalf("fixture ingest status = %d / %v", code, m)
	}
	var setID int64
	if err := s.st.queryRow(`SELECT id FROM prediction_sets ORDER BY id DESC LIMIT 1`).Scan(&setID); err != nil {
		t.Fatal(err)
	}
	var horizonStd int
	if err := s.st.queryRow(`SELECT horizon_std FROM prediction_sets WHERE id=?`, setID).Scan(&horizonStd); err != nil {
		t.Fatal(err)
	}
	if horizonStd != 1 {
		t.Fatalf("fixture horizon_std = %d: the payload's horizon drifted away from the receipt", horizonStd)
	}
	// A standard horizon is what makes h12 part of the checkpoint set, which is what the mark-count
	// assertions elsewhere in this package depend on.
	var marks, h12 int
	if err := s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE set_id=?`, setID).Scan(&marks); err != nil {
		t.Fatal(err)
	}
	if want := len(predCheckpoints); marks != want {
		t.Fatalf("fixture marks = %d, want %d", marks, want)
	}
	if err := s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE set_id=? AND checkpoint='h12'`, setID).Scan(&h12); err != nil {
		t.Fatal(err)
	}
	if h12 != 1 {
		t.Fatal("a standard-horizon fixture must create its h12 mark")
	}
}

// A non-standard horizon must still be expressible, or the derivation above would hide the very
// distinction it exists to preserve: h12 is absent for a caller-supplied horizon.
func TestPredWireLetsACaseChooseANonStandardHorizon(t *testing.T) {
	s := newV1Server(t)
	raw := predWire(func(m map[string]any) {
		m["horizon_end"] = time.Now().UTC().AddDate(3, 0, 0).Format("2006-01-02")
	})
	code, m := ingestPost(t, s, ingestBody(string(raw)))
	if code != 200 || m["predictions_status"] != predStateAccepted {
		t.Fatalf("non-standard ingest status = %d / %v", code, m)
	}
	var setID int64
	if err := s.st.queryRow(`SELECT id FROM prediction_sets ORDER BY id DESC LIMIT 1`).Scan(&setID); err != nil {
		t.Fatal(err)
	}
	var h12 int
	if err := s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE set_id=? AND checkpoint='h12'`, setID).Scan(&h12); err != nil {
		t.Fatal(err)
	}
	if h12 != 0 {
		t.Fatal("a non-standard horizon must not get an h12 mark")
	}
}
