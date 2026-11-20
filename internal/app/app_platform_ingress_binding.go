package app

import (
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Endpoints are credential bindings (doc 180 section 3.2). The helpers here are the review-time
// checks of an ingress_hmac_sha256 binding; the request path is app_platform_ingress_http.go.

// platformIngressRouteDeclared reports whether the release declares the ingress.webhook capability
// and a route of that id.
func platformIngressRouteDeclared(e migExec, release, route string) bool {
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release).Scan(&descriptor); err != nil {
		return false
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != release {
		return false
	}
	_, ok := platformIngressDeclaredRoute(parsed.Manifest, route)
	return ok
}

// platformIngressDeclaredRoute finds a declared route of a manifest that declares the capability.
func platformIngressDeclaredRoute(m appplatform.Manifest, route string) (appplatform.IngressRoute, bool) {
	declares := false
	for _, name := range m.Capabilities.Optional {
		declares = declares || name == "ingress.webhook"
	}
	if !declares || m.Ingress == nil || m.Backend == nil {
		return appplatform.IngressRoute{}, false
	}
	for _, r := range m.Ingress.Routes {
		if r.ID == route {
			return r, true
		}
	}
	return appplatform.IngressRoute{}, false
}

// checkPlatformIngressCredentialValue opens a credential inside custody and checks that its value is
// a shared secret of 32 to 256 bytes, then wipes it. It never reports the value.
func (s *Server) checkPlatformIngressCredentialValue(id string, revision int64, sealed string) error {
	plain, err := s.openPlatformCredentialValue(id, revision, sealed)
	if err != nil {
		return errAppPlatformCredentialUnavailable
	}
	defer clear(plain)
	if len(plain) < appplatform.MinIngressSecret || len(plain) > appplatform.MaxIngressSecret {
		return errPlatformManagementInput
	}
	return nil
}

// platformIngressPlanShape is the part of a binding review that is specific to ingress: an ingress
// review names a route and has no network target, principals or actions; no other review names a route.
func platformIngressPlanShape(plan platformSecretBindingReview) bool {
	if plan.Operation != "ingress_hmac_sha256" {
		return plan.IngressRoute == ""
	}
	return plan.NetworkPolicySHA256 == "" && appplatform.ValidIngressRouteID(plan.IngressRoute) && len(plan.Principals) == 0 && len(plan.ACS3Actions) == 0
}
