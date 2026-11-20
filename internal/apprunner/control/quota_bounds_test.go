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

// The runner refuses a quota it could not safely size (zero, negative, above the platform maximum),
// whatever the controller signed. The lower bound and the 64 MiB grid are Portal's review rules, not the
// runner's: native tests prepare small quotas on purpose.
func TestPrepareDataQuotaIsCheckedAgainstThePlatformBounds(t *testing.T) {
	mib := func(n int64) string { return strconv.FormatInt(n<<20, 10) }
	for name, c := range map[string]struct {
		bytes, inodes string
		ok            bool
	}{
		"default":             {mib(256), "65536", true},
		"market-monitor":      {mib(3072), "100000", true},
		"maximum":             {mib(16384), "1000000", true},
		"below the minimum":   {mib(192), "65536", true},
		"above the maximum":   {mib(16448), "65536", false},
		"off the 64 MiB grid": {mib(300), "65536", true},
		"few inodes":          {mib(256), "128", true},
		"too many inodes":     {mib(256), "1000001", false},
		"overflowing bytes":   {"9223372036854775807", "65536", false},
		"overflowing inodes":  {mib(256), "9223372036854775807", false},
		"past int64":          {"99999999999999999999", "65536", false},
		"native test quota":   {"8388608", "128", true},
		"zero bytes":          {"0", "65536", false},
		"zero inodes":         {mib(256), "0", false},
	} {
		_, err := LogicalDigest(prepareDataWithQuota(c.bytes, c.inodes))
		if (err == nil) != c.ok {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

// A deletion names the namespace's quota too; the runner holds that to the same sizing bounds.
func TestDeleteDataQuotaIsCheckedAgainstThePlatformBounds(t *testing.T) {
	for name, c := range map[string]struct {
		bytes, inodes string
		ok            bool
	}{
		"default":           {"268435456", "65536", true},
		"maximum":           {"17179869184", "1000000", true},
		"zero inodes":       {"268435456", "0", false},
		"too many inodes":   {"268435456", "1000001", false},
		"small bytes":       {"134217728", "65536", true},
		"bytes above":       {"17246978048", "65536", false},
		"bytes off grid":    {"268435968", "65536", true},
		"overflowing inode": {"268435456", "9223372036854775807", false},
	} {
		command, _, _, _ := deleteDataFixture(t)
		command.Payload.QuotaBytes, command.Payload.QuotaInodes = c.bytes, c.inodes
		if _, err := DeleteDataLogicalDigest(command); (err == nil) != c.ok {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}
