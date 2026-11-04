//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const serviceBackendTaskQueryMagic = "PRTQ0001"
const serviceBackendTaskReplyMagic = "PRTR0001"

type ServiceBackendTaskObservation struct {
	result     journal.ServiceBackendTaskResult
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (ServiceBackendTaskObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}

func (o ServiceBackendTaskObservation) Result() journal.ServiceBackendTaskResult {
	result := o.result
	result.Response.Body = bytes.Clone(result.Response.Body)
	return result
}

func (o ServiceBackendTaskObservation) Controller() control.Authority {
	authority := o.authority
	authority.PublicKey = bytes.Clone(authority.PublicKey)
	return authority
}

func (o ServiceBackendTaskObservation) Endpoint() CandidateReceiptEndpoint {
	return o.endpoint
}

func (o ServiceBackendTaskObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o ServiceBackendTaskObservation) CommandSHA256() string { return o.commandSHA }

func RequestControllerServiceTask(
	ctx context.Context,
	path string,
	uid, gid uint32,
	raw, signature []byte,
	authority control.Authority,
	descriptor, body []byte,
) (ServiceBackendTaskObservation, error) {
	if !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return ServiceBackendTaskObservation{}, ErrUnsafeEndpoint
	}
	return requestServiceTaskFrame(
		ctx, path, controllerProfile(gid), raw, signature,
		authority, descriptor, body)
}

func requestServiceTaskFrame(
	ctx context.Context,
	path string,
	profile endpointProfile,
	raw, signature []byte,
	authority control.Authority,
	descriptor, body []byte,
) (ServiceBackendTaskObservation, error) {
	var zero ServiceBackendTaskObservation
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes ||
		len(signature) != 64 || len(descriptor) == 0 ||
		len(descriptor) > appplatform.MaxDescriptorBytes ||
		len(body) == 0 || len(body) > appplatform.MaxTaskBackendBody {
		return zero, driver.ErrContainerUncertain
	}
	raw, signature, descriptor, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(body)
	authority.PublicKey = bytes.Clone(authority.PublicKey)
	defer clear(body)
	started := time.Now()
	verified, err := control.VerifyServiceBackendTask(
		raw, signature, authority, started.UnixMilli())
	if err != nil {
		return zero, err
	}
	if err := control.ValidateServiceBackendTaskData(verified, body); err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil ||
		parsed.SHA256 != verified.RegistrationIdentity().ReleaseSHA256 ||
		!appplatform.TaskHandlerAllowsApp(
			parsed.Manifest.ID, verified.Command().Handler) {
		return zero, driver.ErrContainerUncertain
	}
	ctx, cancel := context.WithTimeout(ctx, backendRequestTimeout)
	defer cancel()
	conn, err := connectEndpoint(ctx, path, profile)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	frame := make([]byte, 44, 44+len(raw)+64+4+len(descriptor)+4+len(body))
	defer func() { clear(frame) }()
	copy(frame, serviceBackendTaskQueryMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return zero, err
	}
	binary.BigEndian.PutUint32(frame[40:44], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, signature...)
	frame = append(frame, candidateDescriptorFrame(descriptor)...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(body)))
	frame = append(frame, size[:]...)
	frame = append(frame, body...)
	if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	if err := conn.CloseWrite(); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	digest := sha256.Sum256(frame)
	count := binary.BigEndian.Uint32(header[40:44])
	if string(header[:8]) != serviceBackendTaskReplyMagic ||
		!bytes.Equal(header[8:40], digest[:]) ||
		count == 0 || count > journal.MaxBackendRequestResultBytes {
		return zero, driver.ErrContainerUncertain
	}
	response := make([]byte, int(count))
	defer clear(response)
	if _, err := io.ReadFull(conn, response); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return zero, driver.ErrContainerUncertain
	}
	result, err := journal.ParseServiceBackendTaskResult(response)
	received := time.Now()
	observed, _ := strconv.ParseInt(result.ObservedMS, 10, 64)
	commandSHA := sha256.Sum256(raw)
	if err != nil || result.RequestID != verified.Command().RequestID ||
		result.TaskID != verified.Command().TaskID ||
		result.LeaseID != verified.Command().LeaseID ||
		result.BootID != verified.Command().BootID ||
		result.RequestSHA256 != hex.EncodeToString(commandSHA[:]) ||
		observed < started.UnixMilli() || observed > received.UnixMilli() ||
		received.Before(started) || ctx.Err() != nil {
		return zero, driver.ErrContainerUncertain
	}
	if _, err := control.VerifyServiceBackendTask(
		raw, signature, authority, received.UnixMilli()); err != nil {
		return zero, err
	}
	return ServiceBackendTaskObservation{
		result: result, authority: authority,
		endpoint: CandidateReceiptEndpoint{
			Path: path, RunnerUID: profile.owner,
			ControllerUID: uint32(os.Geteuid()),
			ControllerGID: profile.group, RootController: profile.shared,
		},
		receivedAt: received, commandSHA: hex.EncodeToString(commandSHA[:]),
	}, nil
}

func (s *Server) handleServiceBackendTask(ctx context.Context, conn *net.UnixConn) {
	provider := s.candidateExecution
	if provider == nil || provider.store == nil || provider.native == nil {
		return
	}
	header := make([]byte, 44)
	copy(header, serviceBackendTaskQueryMagic)
	if _, err := io.ReadFull(conn, header[8:]); err != nil {
		return
	}
	count := binary.BigEndian.Uint32(header[40:44])
	if count == 0 || count > control.MaxCommandBytes {
		return
	}
	command := make([]byte, int(count)+64)
	if _, err := io.ReadFull(conn, command); err != nil {
		return
	}
	descriptor, err := readCandidateDescriptor(conn)
	if err != nil {
		return
	}
	enrollment, err := provider.store.ControllerDeployment(ctx)
	if err != nil {
		return
	}
	authority := control.Authority{
		DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey,
		Fingerprint: enrollment.Fingerprint,
	}
	if _, err := control.VerifyServiceBackendTask(
		command[:count], command[count:], authority, time.Now().UnixMilli()); err != nil {
		return
	}
	var bodySize [4]byte
	if _, err := io.ReadFull(conn, bodySize[:]); err != nil {
		return
	}
	bodyLength := binary.BigEndian.Uint32(bodySize[:])
	if bodyLength == 0 || bodyLength > appplatform.MaxTaskBackendBody {
		return
	}
	body := make([]byte, int(bodyLength))
	defer clear(body)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return
	}
	result, err := provider.store.ForwardServiceBackendTask(
		ctx, command[:count], command[count:], descriptor, body, provider.native)
	if err != nil {
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > journal.MaxBackendRequestResultBytes {
		return
	}
	digest := sha256.New()
	digest.Write(header)
	digest.Write(command)
	digest.Write(candidateDescriptorFrame(descriptor))
	digest.Write(bodySize[:])
	digest.Write(body)
	reply := make([]byte, 44, 44+len(encoded))
	defer clear(reply)
	copy(reply, serviceBackendTaskReplyMagic)
	copy(reply[8:40], digest.Sum(nil))
	binary.BigEndian.PutUint32(reply[40:44], uint32(len(encoded)))
	reply = append(reply, encoded...)
	_, _ = io.Copy(conn, bytes.NewReader(reply))
}
