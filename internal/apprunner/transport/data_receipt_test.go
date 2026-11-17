package transport

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func receiptVector(t *testing.T) journal.DataPreparationReceipt {
	t.Helper()
	raw, err := os.ReadFile("testdata/data-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := journal.ParseDataPreparationReceipt(raw)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
func TestDataReceiptPublicVectorAndOpaqueJSON(t *testing.T) {
	r := receiptVector(t)
	c := control.Command{Format: control.Format, DeploymentID: r.DeploymentID, ControllerEpoch: r.ControllerEpoch, ControlRevision: r.ControlRevision, InstallationID: r.InstallationID, OperationID: "00000000-0000-0000-0000-000000000411", TargetGeneration: r.Generation, Stage: "prepare_data", EffectNumber: "1", EffectID: r.Claim.EffectID, Kind: "prepare-data", InstallationFence: r.Claim.AdmittedFence, PayloadSHA256: r.PayloadSHA256, Payload: control.PrepareDataPayload{DataNamespaceID: r.Claim.ResourceID, ReleaseSHA256: r.ReleaseSHA256, DataFormat: r.DataFormat, QuotaBytes: r.QuotaBytes, QuotaInodes: r.QuotaInodes, WriterFence: r.WriterFence}}
	digest, err := control.LogicalDigest(c)
	if err != nil || digest != r.PayloadSHA256 || !r.MatchesCommand(c) {
		t.Fatal("fixed public ownership/logical vector disagrees", err)
	}
	proof := DataReceiptObservation{receivedAt: time.Now()}
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("proof serialized")
	}
	if err := json.Unmarshal([]byte(`{}`), &proof); err == nil || !proof.ReceivedAt().IsZero() {
		t.Fatal("JSON retained old proof")
	}
}
