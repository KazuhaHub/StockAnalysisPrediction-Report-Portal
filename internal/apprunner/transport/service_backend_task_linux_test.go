//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func serviceTaskFrameFixture(t *testing.T) (control.Authority, control.ServiceBackendTaskCommand, []byte, []byte, []byte, []byte) {
	t.Helper()
	authority, _, registrationRaw, registrationSignature, descriptor, _ := registrationRequestFixture(t)
	registration, err := control.VerifyBackendRegistration(
		registrationRaw, registrationSignature, authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	candidate := registration.Candidate()
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	identity, err := appplatform.SignServiceIdentity(
		bytes.Repeat([]byte{31}, 32), appplatform.ServiceIdentityGrant{
			Format:          appplatform.ServiceIdentityFormat,
			LeaseID:         "11111111-1111-4111-8111-111111119921",
			AppID:           parsed.Manifest.ID,
			InstallationID:  candidate.InstallationID,
			ControllerEpoch: candidate.ControllerEpoch,
			Generation:      "1",
			PolicyRevision:  "1",
			ReleaseSHA256:   candidate.Payload.ReleaseSHA256,
			Resources:       []string{"tasks.schedule"},
			IssuedMS:        strconv.FormatInt(now, 10),
			ExpiresMS:       strconv.FormatInt(now+60000, 10),
		}, now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := appplatform.EncodeTaskBackendRequest(
		"11111111-1111-4111-8111-111111119922",
		"fixture:1", []byte(`{"value":1}`))
	if err != nil {
		t.Fatal(err)
	}
	command := control.ServiceBackendTaskCommand{
		Format:                control.ServiceBackendTaskFormat,
		RequestID:             "11111111-1111-4111-8111-111111119923",
		TaskID:                "11111111-1111-4111-8111-111111119922",
		LeaseID:               "11111111-1111-4111-8111-111111119921",
		InstallationID:        candidate.InstallationID,
		Generation:            "1",
		PolicyRevision:        "1",
		ControllerEpoch:       candidate.ControllerEpoch,
		ReleaseSHA256:         candidate.Payload.ReleaseSHA256,
		BootID:                "boot-service-task",
		RegistrationRaw:       registrationRaw,
		RegistrationSignature: registrationSignature,
		ServiceIdentity:       identity,
	}
	bodySHA := sha256.Sum256(body)
	command.BodySHA256 = hex.EncodeToString(bodySHA[:])
	raw, signature, err := control.SignServiceBackendTask(
		command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)),
		authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return authority, command, raw, signature, descriptor, body
}

func TestServiceBackendTaskFramingAndProvenance(t *testing.T) {
	for _, mode := range []string{
		"accepted", "magic", "nonce", "request_id", "task_id",
		"lease_id", "request_hash", "boot", "status", "body", "trailing",
	} {
		t.Run(mode, func(t *testing.T) {
			authority, command, raw, signature, descriptor, body := serviceTaskFrameFixture(t)
			dir, err := os.MkdirTemp("/tmp", "stask-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "runner.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.AcceptUnix()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				request, err := io.ReadAll(io.LimitReader(conn, 200000))
				defer clear(request)
				if err != nil || len(request) < 44 ||
					string(request[:8]) != serviceBackendTaskQueryMagic {
					return
				}
				sum := sha256.Sum256(raw)
				result := journal.ServiceBackendTaskResult{
					Format:                journal.ServiceBackendTaskResultFormat,
					RequestID:             command.RequestID,
					TaskID:                command.TaskID,
					LeaseID:               command.LeaseID,
					RequestSHA256:         hex.EncodeToString(sum[:]),
					DeliveryReceiptSHA256: strings.Repeat("c", 64),
					NativeID:              strings.Repeat("b", 64),
					BootID:                command.BootID,
					Response: driver.BackendResponse{
						Status: 200,
						Body:   []byte(`{"format":"portal.app-task.response/1-dev.1","outcome":"succeeded","result":{"count":1}}`),
					},
					ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
				}
				switch mode {
				case "request_id":
					result.RequestID = "11111111-1111-4111-8111-111111119924"
				case "task_id":
					result.TaskID = "11111111-1111-4111-8111-111111119925"
				case "lease_id":
					result.LeaseID = "11111111-1111-4111-8111-111111119926"
				case "request_hash":
					result.RequestSHA256 = strings.Repeat("f", 64)
				case "boot":
					result.BootID = "other"
				case "status":
					result.Response.Status = 302
				case "body":
					result.Response.Body = []byte("<html>")
				}
				encoded, _ := json.Marshal(result)
				if mode == "trailing" {
					encoded = append(encoded, 'x')
				}
				header := make([]byte, 44)
				copy(header, serviceBackendTaskReplyMagic)
				digest := sha256.Sum256(request)
				copy(header[8:40], digest[:])
				binary.BigEndian.PutUint32(header[40:44], uint32(len(encoded)))
				if mode == "nonce" {
					header[8] ^= 1
				}
				if mode == "magic" {
					header[0] = 'X'
				}
				_, _ = conn.Write(append(header, encoded...))
			}()
			observation, err := requestServiceTaskFrame(
				context.Background(), path,
				privateProfile(uint32(os.Geteuid())),
				raw, signature, authority, descriptor, body)
			<-done
			if mode == "accepted" {
				if err != nil || observation.ReceivedAt().IsZero() ||
					observation.Endpoint().RootController {
					t.Fatal("private service task response", err)
				}
				result := observation.Result()
				result.Response.Body[0] ^= 1
				if bytes.Equal(
					result.Response.Body,
					observation.Result().Response.Body) {
					t.Fatal("mutable service task response")
				}
				if _, err := json.Marshal(observation); err == nil {
					t.Fatal("service task observation serialized")
				}
			} else if err == nil || !observation.ReceivedAt().IsZero() {
				t.Fatal("unsafe service task response accepted", mode)
			}
		})
	}
}
