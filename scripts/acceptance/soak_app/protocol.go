package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The registration handshake and the signed request check below mirror scripts/acceptance/ready_app, the
// fixture every other native acceptance starts. They are repeated here, not shared, because both are package
// main and the ready fixture is not touched by this change.

func newBootID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
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

// answerRequest verifies the signed request and answers with payload (the POST count when it is nil).
func (s *fixtureRequestState) answerRequest(w http.ResponseWriter, r *http.Request, bootID string, payload func() []byte) {
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
		s.verifier = protocol.NewVerifier(protocol.VerifyConfig{Issuer: signer.Issuer, Audience: signer.Audience, AppID: signer.AppID, InstanceID: signer.InstanceID, BootID: bootID, Keys: map[string][]byte{signer.KeyID: signer.Key}, ReplayCapacity: 4096})
		s.keyID = signer.KeyID
	}
	verifier := s.verifier
	s.mu.Unlock()
	if _, err := verifier.Verify(r.Header.Get(protocol.Header), r.Method, r.URL.RequestURI(), body, time.Now()); err != nil {
		refuse()
		return
	}
	if r.Method == http.MethodPost {
		s.count.Add(1)
	}
	if payload != nil {
		w.Write(payload())
		return
	}
	json.NewEncoder(w).Encode(struct {
		Count int64 `json:"count"`
	}{s.count.Load()})
}
