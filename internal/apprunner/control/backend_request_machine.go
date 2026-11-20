package control

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// validateMachineAssertionData is the same structural consistency for a machine assertion (doc 183):
// it names this boot and this exact path and query, and the request is a bodiless GET. As with a
// viewer assertion this is not authentication; the app's verifier remains mandatory.
func validateMachineAssertionData(boot, method, path, bodyDigest, assertionDigest string, body []byte, assertion string) error {
	claims, err := appplatform.ParseMachineClaims(assertion)
	pathOnly, query, _ := strings.Cut(path, "?")
	if err != nil || method != "GET" || len(body) != 0 || !strings.HasPrefix(pathOnly, "/v1/machine/") || claims.BootID != boot || claims.Method != method || claims.Path != pathOnly || claims.QueryDigest != appplatform.MachineQueryDigest(query) {
		return ErrInvalidCommand
	}
	bodySHA, assertionSHA := sha256.Sum256(body), sha256.Sum256([]byte(assertion))
	if hex.EncodeToString(bodySHA[:]) != bodyDigest || hex.EncodeToString(assertionSHA[:]) != assertionDigest {
		return ErrInvalidCommand
	}
	return nil
}
