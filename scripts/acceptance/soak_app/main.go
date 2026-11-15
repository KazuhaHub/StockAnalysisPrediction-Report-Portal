// Disposable backend fixture for the declared-capacity soak (doc 171, PR 4b).
//
// Besides answering the managed backend protocol (readiness, the registration handshake, signed requests), it
// uses every resource its descriptor declares: it holds most of its memory, burns its CPU share, starts
// threads up to its PID limit, floods its log and fills its data namespace up to the quota. All of it starts
// at boot, from limits it reads from its own cgroup, so the soak needs no control channel into the container.
package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

func main() {
	path := os.Getenv("PORTAL_APP_HTTP_SOCKET")
	if path != "/run/portal-app/http/app.sock" {
		os.Exit(2)
	}
	var served atomic.Int64
	state := newSoakState(&served)
	go state.run()
	handshake := &fixtureRequestState{}
	bootID, err := newBootID()
	if err != nil {
		os.Exit(6)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		os.Exit(3)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		os.Exit(4)
	}
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
		IdleTimeout: 5 * time.Second, MaxHeaderBytes: 65536,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			served.Add(1)
			switch {
			case r.URL.Path == "/v1/echo" && (r.Method == http.MethodGet || r.Method == http.MethodPost) &&
				r.Header.Get("Cookie") == "" && r.Header.Get("Authorization") == "":
				handshake.answerRequest(w, r, bootID, nil)
			case r.URL.Path == "/v1/soak/status" && r.Method == http.MethodGet && r.Header.Get("Cookie") == "" && r.Header.Get("Authorization") == "":
				handshake.answerRequest(w, r, bootID, state.statusJSON)
			case r.Method == http.MethodPost && r.URL.Path == "/v1/handshake" && r.URL.RawQuery == "" &&
				r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "":
				answerFixtureChallenge(w, r, bootID)
			case r.Method == http.MethodGet && r.URL.Path == "/health/ready" && r.URL.RawQuery == "" &&
				r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "":
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(struct {
					Protocol   string `json:"protocol"`
					Ready      bool   `json:"ready"`
					DataFormat string `json:"data_format"`
				}{"portal.backend/1", true, "1"})
			default:
				http.Error(w, "refused", http.StatusBadRequest)
			}
		}),
	}
	if server.Serve(listener) != nil {
		os.Exit(5)
	}
}
