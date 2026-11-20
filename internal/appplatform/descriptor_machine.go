package appplatform

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// validMachine checks the reviewed machine read block (doc 183 section 4.1). Routes are sorted by id
// so the signed bytes have one reading; every path is a backend relay path under /v1/machine/, so
// the relay's own route rules (no handshake, no traversal) apply unchanged.
func validMachine(m Machine) bool {
	if len(m.Routes) < 1 || len(m.Routes) > maxMachineRoutes {
		return false
	}
	paths := map[string]bool{}
	for i, route := range m.Routes {
		if !machineRouteIDPattern.MatchString(route.ID) || (i > 0 && m.Routes[i-1].ID >= route.ID) ||
			!machinePathPattern.MatchString(route.Path) || paths[route.Path] || !ValidBackendRequestRoute("GET", route.Path) ||
			route.ResponseBytes < 1 || route.ResponseBytes > maxMachineResponseBytes ||
			route.TimeoutMS < minMachineTimeoutMS || route.TimeoutMS > maxMachineTimeoutMS ||
			len(route.Query) > maxMachineQueryNames || len(route.Description) > maxMachineDescription || !validMachineDescription(route.Description) {
			return false
		}
		for _, segment := range strings.Split(strings.TrimPrefix(route.Path, "/v1/machine/"), "/") {
			if segment == "" || segment == "." || segment == ".." {
				return false
			}
		}
		paths[route.Path] = true
		for j, name := range route.Query {
			if !machineQueryNamePattern.MatchString(name) || (j > 0 && route.Query[j-1] >= name) {
				return false
			}
		}
	}
	return true
}

// validMachineDescription accepts plain single-line text: it is shown to an administrator and put
// into the generated OpenAPI document, so controls and invalid UTF-8 are refused.
func validMachineDescription(text string) bool {
	return utf8.ValidString(text) && strings.IndexFunc(text, unicode.IsControl) < 0
}
