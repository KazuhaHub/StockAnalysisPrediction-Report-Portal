// Disposable backend fixture for the owned Linux application-platform acceptance.
package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	path := os.Getenv("PORTAL_APP_HTTP_SOCKET")
	if path != "/run/portal-app/http/app.sock" {
		os.Exit(2)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		os.Exit(3)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		os.Exit(4)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health/ready" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			http.Error(w, "refused", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Protocol   string `json:"protocol"`
			Ready      bool   `json:"ready"`
			DataFormat string `json:"data_format"`
		}{"portal.backend/1", true, "1"})
	})}
	if server.Serve(listener) != nil {
		os.Exit(5)
	}
}
