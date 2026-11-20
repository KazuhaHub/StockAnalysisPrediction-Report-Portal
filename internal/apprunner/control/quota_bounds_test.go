package control

import (
	"strconv"
	"strings"
	"testing"
)

func prepareDataWithQuota(bytes, inodes string) Command {
	id := func(n string) string { return "00000000-0000-4000-8000-00000000" + n }
	return Command{
		Format: Format, DeploymentID: id("0001"), ControllerEpoch: id("0002"), ControlRevision: "1", InstallationID: id("0003"),
		OperationID: id("0004"), TargetGeneration: "1", Stage: "prepare_data", EffectNumber: "1", EffectID: id("0005"), Kind: "prepare-data",
		InstallationFence: "1",
		Payload: PrepareDataPayload{DataNamespaceID: id("0006"), ReleaseSHA256: strings.Repeat("a", 64), DataFormat: "1",
			QuotaBytes: bytes, QuotaInodes: inodes, WriterFence: "1"},
	}
}

// The runner refuses a quota outside the platform bounds, whatever the controller signed.
func TestPrepareDataQuotaIsCheckedAgainstThePlatformBounds(t *testing.T) {
	mib := func(n int64) string { return strconv.FormatInt(n<<20, 10) }
	for name, c := range map[string]struct {
		bytes, inodes string
		ok            bool
	}{
		"default":               {mib(256), "65536", true},
		"market-monitor":        {mib(3072), "100000", true},
		"maximum":               {mib(16384), "1000000", true},
		"below the minimum":     {mib(192), "65536", false},
		"above the maximum":     {mib(16448), "65536", false},
		"off the 64 MiB grid":   {mib(300), "65536", false},
		"too few inodes":        {mib(256), "65535", false},
		"too many inodes":       {mib(256), "1000001", false},
		"overflowing bytes":     {"9223372036854775807", "65536", false},
		"overflowing inodes":    {mib(256), "9223372036854775807", false},
		"past int64":            {"99999999999999999999", "65536", false},
		"512-aligned but small": {"1048576", "1024", false},
	} {
		_, err := LogicalDigest(prepareDataWithQuota(c.bytes, c.inodes))
		if (err == nil) != c.ok {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}
