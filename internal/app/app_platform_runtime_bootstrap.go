package app

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	platformRuntimeSocketEnv = "RP_APP_RUNNER_SOCKET"
	platformRuntimeUIDEnv    = "RP_APP_RUNNER_CONTROLLER_UID"
	platformRuntimeGIDEnv    = "RP_APP_RUNNER_CONTROLLER_GID"
)

var errPlatformRuntimeBootstrap = errors.New("invalid application runtime endpoint configuration")

func platformRuntimeEndpointFromEnv(getenv func(string) string) (platformRuntimeEndpoint, bool, error) {
	var endpoint platformRuntimeEndpoint
	path := getenv(platformRuntimeSocketEnv)
	uidRaw := getenv(platformRuntimeUIDEnv)
	gidRaw := getenv(platformRuntimeGIDEnv)
	if path == "" && uidRaw == "" && gidRaw == "" {
		return endpoint, false, nil
	}
	if path == "" || uidRaw == "" || gidRaw == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 107 || strings.ContainsAny(path, "\x00\r\n") {
		return endpoint, true, errPlatformRuntimeBootstrap
	}
	uid, err := strconv.ParseUint(uidRaw, 10, 32)
	if err != nil {
		return endpoint, true, errPlatformRuntimeBootstrap
	}
	gid, err := strconv.ParseUint(gidRaw, 10, 32)
	if err != nil || uid == 0 || gid == 0 || uid == uint64(^uint32(0)) || gid == uint64(^uint32(0)) {
		return endpoint, true, errPlatformRuntimeBootstrap
	}
	return platformRuntimeEndpoint{path: path, controllerUID: uint32(uid), controllerGID: uint32(gid)}, true, nil
}

// The endpoint is deployment infrastructure, not an application setting. A
// partial/invalid bootstrap disables managed execution but never blocks static
// applications or the rest of the Portal.
func configurePlatformRuntimeEndpoint(s *Server) {
	endpoint, configured, err := platformRuntimeEndpointFromEnv(os.Getenv)
	if !configured {
		return
	}
	if err != nil {
		log.Print("application runtime: configured endpoint is invalid; managed execution disabled")
		return
	}
	if err := s.bindPlatformBackendHTTP(endpoint); err != nil {
		log.Print("application runtime: configured endpoint is unavailable; managed execution disabled")
		return
	}
	log.Print("application runtime: trusted local endpoint configured")
}
