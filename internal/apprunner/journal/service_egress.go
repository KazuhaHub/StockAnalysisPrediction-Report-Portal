package journal

import (
	"bytes"
	"context"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// ServiceEgressBinder is the native half of opening an egress session: the
// three physical checks and the socket bind, one per kind of registered
// backend. *driver.CandidateRuntime is the production implementation; tests
// supply their own.
type ServiceEgressBinder interface {
	candidatePreparer
	BindServiceBroker(context.Context, driver.CandidateLaunch, driver.BackendRegistrationSnapshot, bool) (driver.ServiceBroker, error)
	BindServiceBrokerUpdateStart(context.Context, driver.UpdateStartLaunchRequest, driver.UpdateStartLaunchSnapshot, driver.UpdateStartRegistrationSnapshot, bool) (driver.ServiceBroker, error)
	BindServiceBrokerWritable(context.Context, driver.WritableCandidateLaunchRequest, driver.WritableBackendRegistrationSnapshot, bool) (driver.ServiceBroker, error)
}

// OpenServiceEgressBinding checks a signed egress session against the journal
// and the running container, and, when listen is true, binds the broker socket
// of the session's installation generation. It is the journal-and-native
// counterpart of forwarding a service task: the same registration rows, the
// same authority rechecks, the same physical binding checks, with a socket bind
// in place of the forwarded request. It calls the binder exactly once.
//
// Any drift (a different enrollment, an unapplied or superseded registration, a
// launch that no longer matches, a container that is not the registered one)
// refuses the session and, when a socket was bound, closes it again. The result
// is a point in time; every call the session carries is checked afresh.
func (s *Store) OpenServiceEgressBinding(
	ctx context.Context,
	raw, signature, descriptor []byte,
	native ServiceEgressBinder,
	listen bool,
) (driver.ServiceBroker, error) {
	var zero driver.ServiceBroker
	if native == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes ||
		len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	enrollment, err := s.ControllerDeployment(ctx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey,
		Fingerprint: enrollment.Fingerprint,
	}
	verified, err := control.VerifyServiceEgressSession(raw, signature, authority, time.Now().UnixMilli())
	if err != nil {
		return zero, ErrFenceReconciliation
	}
	identity := verified.RegistrationIdentity()
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != identity.ReleaseSHA256 {
		return zero, ErrFenceReconciliation
	}
	switch identity.Format {
	case control.UpdateStartRegistrationFormat:
		return s.bindServiceEgressUpdateStart(ctx, verified, raw, signature, descriptor, native, listen)
	case control.WritableBackendRegistrationFormat:
		return s.bindServiceEgressWritable(ctx, verified, raw, signature, descriptor, native, listen)
	case control.BackendRegistrationFormat:
		return s.bindServiceEgressInitial(ctx, verified, raw, signature, descriptor, native, listen)
	}
	return zero, ErrFenceReconciliation
}

func verifyServiceEgress(raw, signature []byte, a control.Authority) error {
	_, err := control.VerifyServiceEgressSession(raw, signature, a, time.Now().UnixMilli())
	return err
}

func (s *Store) bindServiceEgressInitial(
	ctx context.Context,
	verified control.VerifiedServiceEgressSession,
	raw, signature, descriptor []byte,
	native ServiceEgressBinder,
	listen bool,
) (driver.ServiceBroker, error) {
	var zero driver.ServiceBroker
	initial, ok := verified.InitialRegistration()
	if !ok {
		return zero, ErrFenceReconciliation
	}
	command := verified.Command()
	prepared, err := s.prepareServiceBindingInitial(
		ctx, initial, verified.RegistrationIdentity().ExpiresMS,
		command.RegistrationRaw, command.RegistrationSignature,
		descriptor, "", native,
		func(a control.Authority) error { return verifyServiceEgress(raw, signature, a) })
	if err != nil {
		return zero, err
	}
	defer prepared.close()
	broker, err := native.BindServiceBroker(prepared.ctx, prepared.launch, prepared.binding, listen)
	if err != nil {
		return zero, err
	}
	after, err := s.ReadBackendRegistrationReceipt(prepared.ctx, command.RegistrationRaw, command.RegistrationSignature)
	if err != nil || after != prepared.before || verifyServiceEgress(raw, signature, prepared.facts.source.authority) != nil {
		closeServiceBroker(broker)
		return zero, ErrFenceReconciliation
	}
	return broker, nil
}

func (s *Store) bindServiceEgressUpdateStart(
	ctx context.Context,
	verified control.VerifiedServiceEgressSession,
	raw, signature, descriptor []byte,
	native ServiceEgressBinder,
	listen bool,
) (driver.ServiceBroker, error) {
	var zero driver.ServiceBroker
	update, ok := verified.UpdateStartRegistration()
	if !ok || update.Start().Command().Payload.TargetReleaseSHA256 != verified.Command().ReleaseSHA256 {
		return zero, ErrFenceReconciliation
	}
	command := verified.Command()
	ctx, cancel, err := registrationDeadline(ctx, verified.RegistrationIdentity().ExpiresMS)
	if err != nil {
		return zero, err
	}
	defer cancel()
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	before, err := s.prepareUpdateStartBackendExchange(ctx, command.RegistrationRaw, command.RegistrationSignature)
	if err != nil {
		return zero, err
	}
	request, expected, err := updateStartExpectedSnapshot(before.facts.source.start, before.facts.source.running.Claim, descriptor)
	if err != nil {
		return zero, err
	}
	before.request = request
	before.binding.Launch = expected
	if err := verifyServiceEgress(raw, signature, before.facts.source.start.authority); err != nil {
		return zero, err
	}
	broker, err := native.BindServiceBrokerUpdateStart(ctx, before.request, before.binding.Launch, before.binding, listen)
	if err != nil {
		return zero, err
	}
	tx, after, err := beginUpdateStartRegistration(ctx, s, command.RegistrationRaw, command.RegistrationSignature, "applied")
	if err != nil {
		closeServiceBroker(broker)
		return zero, err
	}
	defer tx.Rollback()
	if !sameUpdateStartRegistrationSource(before.facts, after) || checkUpdateStartRegistrationCurrent(ctx, after) != nil ||
		verifyServiceEgress(raw, signature, after.source.start.authority) != nil || tx.Commit() != nil {
		closeServiceBroker(broker)
		return zero, ErrFenceReconciliation
	}
	return broker, nil
}

func (s *Store) bindServiceEgressWritable(
	ctx context.Context,
	verified control.VerifiedServiceEgressSession,
	raw, signature, descriptor []byte,
	native ServiceEgressBinder,
	listen bool,
) (driver.ServiceBroker, error) {
	var zero driver.ServiceBroker
	registration, ok := verified.WritableRegistration()
	if !ok || registration.Candidate().Index().Payload.ReleaseSHA256 != verified.Command().ReleaseSHA256 {
		return zero, ErrFenceReconciliation
	}
	command := verified.Command()
	ctx, cancel, err := registrationDeadline(ctx, verified.RegistrationIdentity().ExpiresMS)
	if err != nil {
		return zero, err
	}
	defer cancel()
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	before, err := s.prepareWritableBackendExchange(ctx, command.RegistrationRaw, command.RegistrationSignature, descriptor)
	if err != nil {
		return zero, err
	}
	return s.bindPreparedServiceEgressWritable(ctx, before, raw, signature, native, listen)
}

// bindPreparedServiceEgressWritable is the part of the writable path after the
// registration exchange was prepared: verify, bind, recheck. It is separate so
// the journal half can be tested without a native runtime.
func (s *Store) bindPreparedServiceEgressWritable(
	ctx context.Context,
	before writableBackendExchangePreflight,
	raw, signature []byte,
	native ServiceEgressBinder,
	listen bool,
) (driver.ServiceBroker, error) {
	var zero driver.ServiceBroker
	if err := verifyServiceEgress(raw, signature, before.facts.source.authority); err != nil || s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	broker, err := native.BindServiceBrokerWritable(ctx, before.request, before.binding, listen)
	if err != nil {
		return zero, err
	}
	tx, f, err := s.beginWritableRegistration(ctx, before.facts.raw, before.facts.signature, "applied")
	if err != nil {
		closeServiceBroker(broker)
		return zero, err
	}
	defer tx.Rollback()
	if s.closing.Load() || !sameWritableRegistrationSource(before.facts, f) ||
		!writableSnapshotMatches(before.binding.Launch, f.source, f.running.Claim) ||
		checkWritableRegistrationCurrent(ctx, f) != nil ||
		verifyServiceEgress(raw, signature, f.source.authority) != nil || tx.Commit() != nil {
		closeServiceBroker(broker)
		return zero, ErrFenceReconciliation
	}
	return broker, nil
}

func closeServiceBroker(b driver.ServiceBroker) {
	if b.Listener != nil {
		_ = b.Listener.Close()
	}
}

// registrationDeadline bounds ctx by the registration's own expiry, as the task
// path does: a registration that has expired proves nothing.
func registrationDeadline(ctx context.Context, expiresMS string) (context.Context, context.CancelFunc, error) {
	expires, err := strconv.ParseInt(expiresMS, 10, 64)
	if err != nil || expires <= time.Now().UnixMilli() {
		return nil, nil, ErrFenceReconciliation
	}
	bounded, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	return bounded, cancel, nil
}
