package transport

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func TestDataQueryTransportActionAndReplyBinding(t *testing.T) {
	a, load, _, _, descriptor, _ := loadImageTransportFixture(t)
	now := time.Now().UnixMilli()
	command := control.DataQueryCommand{CommandHeader: load.CommandHeader, Payload: control.DataQueryPayload{
		ReleaseSHA256: load.Payload.ReleaseSHA256, Operation: control.DataQueryInspectImport,
		ArchiveSHA256: strings.Repeat("a", 64), QuotaBytes: "0", QuotaInodes: "100000",
	}}
	command.Format, command.Kind, command.Stage, command.EffectNumber, command.EffectID = control.DataQueryFormat, control.DataQueryKind, control.DataQueryStage, "1", loadImageTransportID(37)
	var err error
	command.PayloadSHA256, err = control.DataQueryLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignDataQuery(command, migrateTransportKey(), a, now)
	if err != nil {
		t.Fatal(err)
	}
	if !DataQueryAnswer.valid() || !bulkDataAction(DataQueryAnswer) {
		t.Fatal("a data query is a valid, bulk action")
	}
	ref, err := verifyWritableLifecycle(DataQueryAnswer, raw, sig, descriptor, a, now)
	if err != nil || ref.dataQuery.EffectID != command.EffectID {
		t.Fatal("data query reference", err)
	}
	report := journal.DataQueryReport{
		Format: "portal.runner.data-query-report/1-dev.1", DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		Operation: command.Payload.Operation, ArchiveFound: true, ArchiveBytes: "4096", ArchiveEntries: "3", ContentBytes: "100",
		SchemaMarker: "1", CapacityRequired: "0", CapacityLeft: "0", ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	body, _ := json.Marshal(report)
	if !validWritableLifecycleReply(DataQueryAnswer, body, ref, time.Now().Add(-time.Second), time.Now().Add(time.Second)) {
		t.Fatal("the exact report was refused")
	}
	report.Operation = control.DataQueryRemoveImport
	body, _ = json.Marshal(report)
	if validWritableLifecycleReply(DataQueryAnswer, body, ref, time.Now().Add(-time.Second), time.Now().Add(time.Second)) {
		t.Fatal("a report of another operation was accepted")
	}
}
