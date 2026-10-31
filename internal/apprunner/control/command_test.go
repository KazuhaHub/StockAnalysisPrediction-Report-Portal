package control

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

func TestRunnerCommandVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/commands.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		LogicalSHA string `json:"logical_sha256"`
		Cases      []struct {
			Name      string `json:"name"`
			Accept    bool   `json:"accept"`
			Raw       string `json:"body_base64"`
			Signature string `json:"signature_base64"`
			Now       string `json:"now_ms"`
			Authority struct {
				DeploymentID string `json:"deployment_id"`
				Epoch        string `json:"controller_epoch"`
				Revision     int64  `json:"control_revision"`
				PublicKey    string `json:"public_key"`
				Fingerprint  string `json:"fingerprint"`
			} `json:"authority"`
		} `json:"cases"`
	}
	if json.Unmarshal(raw, &corpus) != nil {
		t.Fatal("invalid fixture")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != commandCorpusSHA256 {
		t.Fatal("runner command corpus drift")
	}
	if len(corpus.Cases) != 48 {
		t.Fatalf("unexpected case count: %d", len(corpus.Cases))
	}
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			body, err := base64.StdEncoding.Strict().DecodeString(c.Raw)
			if err != nil {
				t.Fatal(err)
			}
			sig, err := base64.StdEncoding.Strict().DecodeString(c.Signature)
			if err != nil {
				t.Fatal(err)
			}
			key, err := base64.StdEncoding.Strict().DecodeString(c.Authority.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			now, err := strconv.ParseInt(c.Now, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := Verify(body, sig, Authority{DeploymentID: c.Authority.DeploymentID, ControllerEpoch: c.Authority.Epoch, ControlRevision: c.Authority.Revision, PublicKey: key, Fingerprint: c.Authority.Fingerprint}, now)
			if (err == nil) != c.Accept {
				t.Fatalf("accept=%v got %v", c.Accept, err)
			}
			if c.Accept && verified.Command().PayloadSHA256 != corpus.LogicalSHA {
				t.Fatal("renewal/fence changed logical identity")
			}
		})
	}
}

const commandCorpusSHA256 = "6134a1b6cf5e7a5b4fb86ae6cf20860a118da05320dc9c01ac22c46340295d80"
