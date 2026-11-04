package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformServiceTaskIntent struct {
	history     platformServiceBackendHistory
	description []byte
	controlSeed []byte
}

type platformServiceBackendHistory struct {
	format         string
	registration   appplatform.BackendRegistration
	raw, signature []byte
	descriptor     []byte
	runtime        platformRuntimeRegistration
	authority      control.Authority
	initial        *platformBackendDeliveryIntent
	update         *platformWritableRegistrationIntent
}

func (h *platformServiceBackendHistory) Close() {
	if h == nil {
		return
	}
	h.registration.Close()
	h.raw, h.signature, h.descriptor = nil, nil, nil
}

func (s *Server) preparePlatformServiceTask(
	ctx context.Context, task platformTaskLease, identity []byte,
) (platformServiceTaskIntent, error) {
	var zero platformServiceTaskIntent
	if s == nil || s.st == nil || task.Generation < 1 ||
		task.PolicyRevision < 1 || len(identity) == 0 {
		return zero, errAppPlatformTaskUnavailable
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	facts, err := loadPlatformServiceFacts(
		e, task.InstallationID, []string{"tasks.schedule"}, true)
	if err != nil ||
		facts.generation != strconv.FormatInt(task.Generation, 10) ||
		facts.policyRevision != strconv.FormatInt(task.PolicyRevision, 10) ||
		facts.release != task.ReleaseSHA256 ||
		facts.epoch != task.ControllerEpoch {
		return zero, errAppPlatformTaskUnavailable
	}
	var activation, release, namespace, readiness string
	var generation, policy, format, observed, fence int64
	var runtimeID sql.NullString
	if err := e.queryRow(`SELECT id,release_sha256,data_namespace_id,
		generation,policy_revision,data_format,observed_ms,readiness_evidence
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`, task.InstallationID).
		Scan(&activation, &release, &namespace, &generation, &policy,
			&format, &observed, &readiness); err != nil ||
		release != task.ReleaseSHA256 || generation != task.Generation ||
		policy != task.PolicyRevision || format < 1 {
		return zero, errAppPlatformTaskUnavailable
	}
	if err := e.queryRow(`SELECT runtime_id,control_fence
		FROM app_platform_installations
		WHERE id=? AND desired_state='enabled' AND phase='ready'
		AND generation=? AND policy_revision=? AND controller_epoch=?`,
		task.InstallationID, task.Generation, task.PolicyRevision,
		task.ControllerEpoch).Scan(&runtimeID, &fence); err != nil ||
		!runtimeID.Valid {
		return zero, errAppPlatformTaskUnavailable
	}
	dek, err := s.readPlatformDEK(e)
	if err != nil {
		return zero, errAppPlatformTaskUnavailable
	}
	defer clear(dek)
	runtime, controlSeed, err := s.platformDataRuntime(
		e, task.ControllerEpoch, dek)
	if err != nil || runtime.id != runtimeID.String {
		clear(controlSeed)
		return zero, errAppPlatformTaskUnavailable
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		clear(controlSeed)
		return zero, err
	}
	history := platformServiceBackendHistory{
		runtime: runtime, authority: platformRuntimeAuthority(runtime),
	}
	if task.Generation == 1 {
		delivery, err := s.loadPlatformManagedBackendHistory(
			e, task.InstallationID, activation, release, namespace,
			task.ControllerEpoch, format, fence, observed, readiness,
			runtime, controlSeed, dek, now)
		if err != nil {
			clear(controlSeed)
			return zero, errAppPlatformTaskUnavailable
		}
		history.format = control.BackendRegistrationFormat
		history.registration = delivery.registration
		history.raw, history.signature = delivery.raw, delivery.signature
		history.descriptor = bytes.Clone(delivery.candidate.descriptor)
		history.runtime = delivery.candidate.runtime
		history.authority = delivery.candidate.authority
		history.initial = &delivery
	} else {
		update, err := s.loadPlatformWritableUpdateBackendHistory(
			e, platformBackendHistoryBinding{
				installation: task.InstallationID, activation: activation,
				release: release, namespace: namespace,
				epoch: task.ControllerEpoch, readiness: readiness,
				format: format, fence: fence, observed: observed,
				generation: task.Generation,
			}, runtime, controlSeed, dek, now)
		if err != nil {
			clear(controlSeed)
			return zero, errAppPlatformTaskUnavailable
		}
		history.format = control.UpdateStartRegistrationFormat
		history.registration = update.registration
		history.raw, history.signature = update.raw, update.signature
		history.descriptor = bytes.Clone(update.start.writer.descriptor)
		history.runtime = update.start.writer.runtime
		history.authority = update.start.writer.authority
		history.update = &update
	}
	failed := true
	defer func() {
		if failed {
			history.Close()
			clear(controlSeed)
		}
	}()
	parsed, err := appplatform.ParseDescriptor(history.descriptor)
	if err != nil || parsed.Manifest.Backend == nil ||
		parsed.SHA256 != release ||
		parsed.Manifest.ID != task.AppID ||
		parsed.Manifest.Publisher != task.PublisherID {
		return zero, errAppPlatformTaskUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	failed = false
	return platformServiceTaskIntent{
		history: history, description: bytes.Clone(history.descriptor),
		controlSeed: controlSeed,
	}, nil
}

func (s *Server) runPlatformBackendTask(
	ctx context.Context, task platformTaskLease, identity string,
) appplatform.TaskExecutionResult {
	return s.runPlatformServiceTask(
		ctx, task, identity, appplatform.TaskHandlerBackend,
		platformTaskBackendBody(task))
}

func (s *Server) runPlatformDigestTask(
	ctx context.Context, task platformTaskLease, identity string,
) appplatform.TaskExecutionResult {
	return s.runPlatformServiceTask(
		ctx, task, identity, appplatform.TaskHandlerDigest,
		platformTaskBackendBody(task))
}

func platformTaskBackendBody(task platformTaskLease) platformTaskBodyBuilder {
	return func() ([]byte, string, error) {
		body, err := appplatform.EncodeTaskBackendRequest(
			task.ID, task.OccurrenceID, task.Payload)
		if err != nil {
			return nil, "backend_request_invalid", err
		}
		return body, "", nil
	}
}

func (s *Server) runPlatformEventDelivery(
	ctx context.Context, task platformTaskLease, identity string,
) appplatform.TaskExecutionResult {
	return s.runPlatformServiceTask(
		ctx, task, identity, appplatform.TaskHandlerEvent,
		func() ([]byte, string, error) {
			if _, err := appplatform.ParseEventDelivery(task.Payload); err != nil {
				return nil, "event_delivery_invalid", err
			}
			return bytes.Clone(task.Payload), "", nil
		})
}

type platformTaskBodyBuilder func() ([]byte, string, error)

func (s *Server) runPlatformServiceTask(
	ctx context.Context,
	task platformTaskLease,
	identity, handler string,
	buildBody platformTaskBodyBuilder,
) appplatform.TaskExecutionResult {
	endpoint := s.platformBackendEndpoint.Load()
	_, handlerPath := appplatform.TaskHandlerPath(handler)
	if endpoint == nil || endpoint.path == "" || !handlerPath {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultFailed,
			ErrorCode: "backend_transport_unavailable",
		}
	}
	if _, err := s.verifyPlatformServiceIdentity(
		ctx, []byte(identity), "tasks.schedule"); err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultUncertain,
			ErrorCode: "task_authority_revoked",
		}
	}
	body, errorCode, err := buildBody()
	if err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultFailed,
			ErrorCode: errorCode,
		}
	}
	defer clear(body)
	intent, err := s.preparePlatformServiceTask(ctx, task, []byte(identity))
	if err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultRetry,
			ErrorCode: "backend_preflight_unavailable",
		}
	}
	defer intent.history.Close()
	defer clear(intent.controlSeed)
	authority := intent.history.authority
	authority.PublicKey = bytes.Clone(authority.PublicKey)
	signer, err := intent.history.registration.Signer("")
	if err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultRetry,
			ErrorCode: "backend_handshake_unavailable",
		}
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultRetry,
			ErrorCode: "backend_handshake_unavailable",
		}
	}
	answer, err := platformHandshakeServiceBackend(
		ctx, *endpoint, &intent.history, challenge)
	if err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultRetry,
			ErrorCode: "backend_handshake_unavailable",
		}
	}
	boot, err := signer.VerifyAnswer(challenge, answer, time.Now())
	if err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultUncertain,
			ErrorCode: "backend_handshake_uncertain",
		}
	}
	if _, err := s.verifyPlatformServiceIdentity(
		ctx, []byte(identity), "tasks.schedule"); err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultUncertain,
			ErrorCode: "task_authority_revoked",
		}
	}
	requestID, err := platformNewUUID()
	if err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultRetry,
			ErrorCode: "backend_request_unavailable",
		}
	}
	command := control.ServiceBackendTaskCommand{
		Format:    control.ServiceBackendTaskFormat,
		RequestID: requestID, TaskID: task.ID, LeaseID: task.LeaseID,
		Handler:               handler,
		InstallationID:        task.InstallationID,
		Generation:            strconv.FormatInt(task.Generation, 10),
		PolicyRevision:        strconv.FormatInt(task.PolicyRevision, 10),
		ControllerEpoch:       task.ControllerEpoch,
		ReleaseSHA256:         task.ReleaseSHA256,
		BootID:                boot,
		RegistrationRaw:       bytes.Clone(intent.history.raw),
		RegistrationSignature: bytes.Clone(intent.history.signature),
		ServiceIdentity:       bytes.Clone([]byte(identity)),
		BodySHA256:            platformDigest(body),
	}
	key := ed25519.NewKeyFromSeed(intent.controlSeed)
	defer clear(key)
	commandRaw, signature, err := control.SignServiceBackendTask(
		command, key, authority, time.Now().UnixMilli())
	if err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultFailed,
			ErrorCode: "backend_request_invalid",
		}
	}
	observation, err := requestPlatformServiceBackendTask(
		ctx, *endpoint, authority, commandRaw, signature,
		intent.description, body)
	if err != nil {
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultUncertain,
			ErrorCode: "backend_response_uncertain",
		}
	}
	result := observation.Result()
	switch result.Response.Status {
	case 200, 201, 202, 204:
		execution, err := appplatform.DecodeTaskBackendResponse(
			result.Response.Body)
		if err != nil {
			return appplatform.TaskExecutionResult{
				Outcome:   appplatform.TaskResultUncertain,
				ErrorCode: "backend_response_invalid",
			}
		}
		execution.EvidenceSHA = platformDigest(result.Response.Body)
		return execution
	case 429:
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultRetry,
			ErrorCode: "backend_rate_limited",
		}
	case 400, 403, 404, 409, 422:
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultFailed,
			ErrorCode: "backend_rejected",
		}
	default:
		return appplatform.TaskExecutionResult{
			Outcome:   appplatform.TaskResultUncertain,
			ErrorCode: "backend_response_uncertain",
		}
	}
}

func requestPlatformServiceBackendTask(
	ctx context.Context,
	endpoint platformRuntimeEndpoint,
	authority control.Authority,
	raw, signature, descriptor, body []byte,
) (transport.ServiceBackendTaskObservation, error) {
	return transport.RequestControllerServiceTask(
		ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		raw, signature, authority, descriptor, body)
}
