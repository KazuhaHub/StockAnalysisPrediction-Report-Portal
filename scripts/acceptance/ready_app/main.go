// Disposable backend fixture for the owned Linux application-platform acceptance.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if err := verifyFixtureSeccomp(); err != nil {
		os.Stderr.WriteString("native fixture seccomp verification failed: " + err.Error() + "\n")
		os.Exit(7)
	}
	if os.Getenv("PORTAL_APP_WRITER_PROBE") == "1" || fixtureHasWritableData() {
		if err := verifyFixtureWritableData(); err != nil {
			os.Stderr.WriteString("native fixture writable data verification failed: " + err.Error() + "\n")
			os.Exit(8)
		}
	}
	var bootNonce [16]byte
	if _, err := rand.Read(bootNonce[:]); err != nil {
		os.Exit(6)
	}
	bootID := hex.EncodeToString(bootNonce[:])
	state := &fixtureRequestState{}
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
	server := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 65536, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/echo" && (r.Method == http.MethodGet || r.Method == http.MethodPost) && r.Header.Get("Cookie") == "" && r.Header.Get("Authorization") == "" {
			state.answerRequest(w, r, bootID)
			return
		}
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

type fixtureRequestState struct {
	mu       sync.Mutex
	keyID    string
	verifier *protocol.Verifier
	count    atomic.Int64
}

func (s *fixtureRequestState) answerRequest(w http.ResponseWriter, r *http.Request, bootID string) {
	w.Header().Set("Content-Type", "application/json")
	refuse := func() { w.WriteHeader(http.StatusForbidden); w.Write([]byte(`{"error":"forbidden"}`)) }
	body, err := io.ReadAll(io.LimitReader(r.Body, appplatform.MaxBackendRequestBody+1))
	defer clear(body)
	if err != nil || len(body) > appplatform.MaxBackendRequestBody {
		refuse()
		return
	}
	file, err := os.Open("/run/portal-app/broker/registration.json")
	if err != nil {
		refuse()
		return
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, 4097))
	defer clear(document)
	if err != nil {
		refuse()
		return
	}
	registration, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		refuse()
		return
	}
	defer registration.Close()
	signer, err := registration.Signer(bootID)
	if err != nil {
		refuse()
		return
	}
	defer clear(signer.Key)
	s.mu.Lock()
	if s.verifier == nil || s.keyID != signer.KeyID {
		s.verifier = protocol.NewVerifier(protocol.VerifyConfig{Issuer: signer.Issuer, Audience: signer.Audience, AppID: signer.AppID, InstanceID: signer.InstanceID, BootID: bootID, Keys: map[string][]byte{signer.KeyID: signer.Key}, ReplayCapacity: 64})
		s.keyID = signer.KeyID
	}
	verifier := s.verifier
	s.mu.Unlock()
	claims, err := verifier.Verify(r.Header.Get(protocol.Header), r.Method, r.URL.RequestURI(), body, time.Now())
	if err != nil {
		refuse()
		return
	}
	if r.Method == http.MethodPost {
		s.count.Add(1)
	}
	w.Header().Set("Set-Cookie", "must-not-escape=1")
	json.NewEncoder(w).Encode(struct {
		Count  int64           `json:"count"`
		Viewer protocol.Viewer `json:"viewer"`
	}{s.count.Load(), claims.Viewer})
}
