package appplatform

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
)

const ServiceIdentityFormat = "portal.app.service-identity/1-dev.1"

var ErrServiceIdentity = errors.New("application service identity unavailable")

// ServiceIdentityGrant contains no account, role or viewer identity. It binds
// finite background authority to one current installation tuple and lease.
type ServiceIdentityGrant struct {
	Format          string   `json:"format"`
	LeaseID         string   `json:"lease_id"`
	AppID           string   `json:"app_id"`
	InstallationID  string   `json:"installation_id"`
	ControllerEpoch string   `json:"controller_epoch"`
	Generation      string   `json:"generation"`
	PolicyRevision  string   `json:"policy_revision"`
	ReleaseSHA256   string   `json:"release_sha256"`
	Resources       []string `json:"resources"`
	IssuedMS        string   `json:"issued_ms"`
	ExpiresMS       string   `json:"expires_ms"`
	Mac             string   `json:"mac"`
}

func normalizeServiceResources(resources []string) ([]string, error) {
	if len(resources) < 1 || len(resources) > 16 {
		return nil, ErrServiceIdentity
	}
	out := append([]string(nil), resources...)
	sort.Strings(out)
	for i, resource := range out {
		if !serviceResource(resource) || i > 0 && out[i-1] == resource {
			return nil, ErrServiceIdentity
		}
	}
	return out, nil
}

func serviceResource(value string) bool {
	if len(value) < 1 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, b := range []byte(value) {
		if !(b >= 'a' && b <= 'z') && !(b >= '0' && b <= '9') && b != '.' && b != '-' {
			return false
		}
	}
	return true
}

func validServiceGrant(g ServiceIdentityGrant, now int64) bool {
	issued, issuedErr := strconv.ParseInt(g.IssuedMS, 10, 64)
	expires, expiresErr := strconv.ParseInt(g.ExpiresMS, 10, 64)
	if issuedErr != nil || expiresErr != nil || issued < 0 || expires <= issued || expires-issued > 300000 || now < issued || now >= expires {
		return false
	}
	for _, counter := range []string{g.Generation, g.PolicyRevision} {
		value, err := strconv.ParseInt(counter, 10, 64)
		if err != nil || value < 1 || strconv.FormatInt(value, 10) != counter {
			return false
		}
	}
	if g.Format != ServiceIdentityFormat || !slugPattern.MatchString(g.AppID) || !backendUUID.MatchString(g.LeaseID) || !backendUUID.MatchString(g.InstallationID) || !backendUUID.MatchString(g.ControllerEpoch) || !digestPattern.MatchString(g.ReleaseSHA256) {
		return false
	}
	_, err := normalizeServiceResources(g.Resources)
	return err == nil
}

func serviceGrantMAC(seed []byte, g ServiceIdentityGrant) (string, error) {
	copy := g
	copy.Resources = append([]string(nil), g.Resources...)
	sort.Strings(copy.Resources)
	copy.Mac = ""
	raw, err := json.Marshal(copy)
	if err != nil || len(raw) > 8192 {
		return "", ErrServiceIdentity
	}
	mac := hmac.New(sha256.New, seed)
	mac.Write([]byte("portal.app-service-identity/1\x00"))
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func SignServiceIdentity(seed []byte, g ServiceIdentityGrant, now int64) ([]byte, error) {
	if len(seed) != 32 || !validServiceGrant(g, now) || g.Mac != "" {
		return nil, ErrServiceIdentity
	}
	resources, err := normalizeServiceResources(g.Resources)
	if err != nil {
		return nil, err
	}
	g.Resources = resources
	mac, err := serviceGrantMAC(seed, g)
	if err != nil {
		return nil, err
	}
	g.Mac = mac
	raw, err := json.Marshal(g)
	if err != nil || len(raw) > 8192 {
		return nil, ErrServiceIdentity
	}
	return raw, nil
}

func VerifyServiceIdentity(raw, seed []byte, expected ServiceIdentityGrant, requiredResource string, now int64) (ServiceIdentityGrant, error) {
	var zero ServiceIdentityGrant
	if len(seed) != 32 || len(raw) == 0 || len(raw) > 8192 || !serviceResource(requiredResource) {
		return zero, ErrServiceIdentity
	}
	var g ServiceIdentityGrant
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&g) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validServiceGrant(g, now) || len(g.Mac) != 64 {
		return zero, ErrServiceIdentity
	}
	resources, err := normalizeServiceResources(g.Resources)
	if err != nil || strings.Join(resources, "\x00") != strings.Join(g.Resources, "\x00") {
		return zero, ErrServiceIdentity
	}
	canonical, err := json.Marshal(g)
	if err != nil || !bytes.Equal(canonical, raw) {
		return zero, ErrServiceIdentity
	}
	mac, err := serviceGrantMAC(seed, g)
	expectedMAC, decodeErr := hex.DecodeString(g.Mac)
	actualMAC, actualErr := hex.DecodeString(mac)
	if err != nil || decodeErr != nil || actualErr != nil || !hmac.Equal(expectedMAC, actualMAC) {
		return zero, ErrServiceIdentity
	}
	if expected.Format != "" && g.Format != expected.Format ||
		expected.LeaseID != "" && g.LeaseID != expected.LeaseID ||
		expected.AppID != "" && g.AppID != expected.AppID ||
		expected.InstallationID != "" && g.InstallationID != expected.InstallationID ||
		expected.ControllerEpoch != "" && g.ControllerEpoch != expected.ControllerEpoch ||
		expected.Generation != "" && g.Generation != expected.Generation ||
		expected.PolicyRevision != "" && g.PolicyRevision != expected.PolicyRevision ||
		expected.ReleaseSHA256 != "" && g.ReleaseSHA256 != expected.ReleaseSHA256 {
		return zero, ErrServiceIdentity
	}
	found := false
	for _, resource := range g.Resources {
		found = found || resource == requiredResource
	}
	if !found {
		return zero, ErrServiceIdentity
	}
	return g, nil
}
