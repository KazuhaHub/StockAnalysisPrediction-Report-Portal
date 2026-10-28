// Disposable backend fixture for the owned Linux application-platform acceptance.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	var bootNonce [16]byte
	if _, err := rand.Read(bootNonce[:]); err != nil {
		os.Exit(6)
	}
	bootID := hex.EncodeToString(bootNonce[:])
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
		if r.Method == http.MethodPost && r.URL.Path == "/v1/handshake" && r.URL.RawQuery == "" && r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "" {
			answerFixtureChallenge(w, r, bootID)
			return
		}
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

func answerFixtureChallenge(w http.ResponseWriter, r *http.Request, bootID string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	challenge, err := appplatform.ParseBackendHandshakeChallenge(body)
	if err != nil {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	file, err := os.Open("/run/portal-app/broker/registration.json")
	if err != nil {
		http.Error(w, "not registered", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, 4097))
	defer clear(document)
	if err != nil {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	registration, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	defer registration.Close()
	signer, err := registration.Signer(bootID)
	if err != nil {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	defer clear(signer.Key)
	answer, err := signer.AnswerChallenge(challenge, bootID, time.Now())
	if err != nil {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Answer string `json:"answer"`
	}{answer})
}
