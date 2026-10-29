package transport

import (
	"bytes"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// WritableLifecycleAction selects a closed infrastructure operation. It never
// accepts a native path, Docker option, callback or caller-provided success flag.
type WritableLifecycleAction byte

const (
	WritableWriterExecute WritableLifecycleAction = iota + 1
	WritableWriterRead
	WritableWriterReconcile
	WritableCandidateStart
	WritableCandidateRead
	WritableCandidateReconcile
	WritableCandidateProbe
	WritableRegistrationDeliver
	WritableRegistrationRead
)

func (a WritableLifecycleAction) valid() bool {
	return a >= WritableWriterExecute && a <= WritableRegistrationRead
}
func (a WritableLifecycleAction) mutating() bool {
	return a == WritableWriterExecute || a == WritableWriterReconcile || a == WritableCandidateStart || a == WritableCandidateReconcile || a == WritableRegistrationDeliver
}

// This opaque value records authenticated response provenance. Historical
// receipts are not health/boot/access grants. Typed accessors parse fresh copies
// so nested pointer fields cannot mutate the retained canonical evidence.
type WritableLifecycleObservation struct {
	action     WritableLifecycleAction
	body       string
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (WritableLifecycleObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}
func (o *WritableLifecycleObservation) UnmarshalJSON([]byte) error {
	*o = WritableLifecycleObservation{}
	return driver.ErrContainerUncertain
}
func (o WritableLifecycleObservation) Action() WritableLifecycleAction { return o.action }
func (o WritableLifecycleObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o WritableLifecycleObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o WritableLifecycleObservation) ReceivedAt() time.Time              { return o.receivedAt }
func (o WritableLifecycleObservation) CommandSHA256() string              { return o.commandSHA }
func (o WritableLifecycleObservation) WriterReceipt() (journal.DataWriterReceipt, error) {
	if o.action < WritableWriterExecute || o.action > WritableWriterReconcile {
		return journal.DataWriterReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseDataWriterReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) RunningReceipt() (journal.WritableCandidateRunningReceipt, error) {
	if o.action < WritableCandidateStart || o.action > WritableCandidateReconcile {
		return journal.WritableCandidateRunningReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseWritableCandidateRunningReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) Readiness() (journal.WritableCandidateReadiness, error) {
	if o.action != WritableCandidateProbe {
		return journal.WritableCandidateReadiness{}, driver.ErrContainerUncertain
	}
	return journal.ParseWritableCandidateReadiness([]byte(o.body))
}
func (o WritableLifecycleObservation) RegistrationReceipt() (journal.WritableBackendRegistrationReceipt, error) {
	if o.action != WritableRegistrationDeliver && o.action != WritableRegistrationRead {
		return journal.WritableBackendRegistrationReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseWritableBackendRegistrationReceipt([]byte(o.body))
}

type writableLifecycleReference struct {
	writer       control.DataWriterCommand
	candidate    control.WritableCandidateCommand
	registration control.WritableBackendRegistrationCommand
	index        control.Command
	release      string
}

func verifyWritableLifecycle(action WritableLifecycleAction, raw, sig, descriptor []byte, a control.Authority, now int64) (writableLifecycleReference, error) {
	var r writableLifecycleReference
	if !action.valid() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(sig) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return r, driver.ErrContainerUncertain
	}
	switch {
	case action <= WritableWriterReconcile:
		v, err := control.VerifyDataWriter(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.writer = v.Command()
		r.index = v.Data()
		r.release = r.index.Payload.ReleaseSHA256
	case action <= WritableCandidateProbe:
		v, err := control.VerifyWritableCandidate(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.candidate = v.Command()
		r.writer = v.Writer().Command()
		r.index = v.Writer().Data()
		r.release = v.Index().Payload.ReleaseSHA256
	default:
		v, err := control.VerifyWritableBackendRegistration(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.registration = v.Command()
		r.candidate = v.Candidate().Command()
		r.writer = v.Candidate().Writer().Command()
		r.index = v.Candidate().Writer().Data()
		r.release = v.Candidate().Index().Payload.ReleaseSHA256
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != r.release {
		return writableLifecycleReference{}, driver.ErrContainerUncertain
	}
	return r, nil
}

// Each independently parsed canonical reply must match its exact signed action.
// This check supplements native/journal validation; it cannot replace it.
func validWritableLifecycleReply(action WritableLifecycleAction, body []byte, r writableLifecycleReference, started, received time.Time) bool {
	if received.Before(started) {
		return false
	}
	var settled string
	switch {
	case action >= WritableWriterExecute && action <= WritableWriterReconcile:
		v, e := journal.ParseDataWriterReceipt(body)
		if e != nil || v.Claim.EffectID != r.writer.EffectID || v.Claim.PayloadSHA256 != r.writer.PayloadSHA256 || v.Claim.DataReceiptSHA256 != r.writer.DataReceiptSHA256 || v.Claim.ResourceID != r.writer.ResourceID || v.Claim.AdmittedFence != r.index.InstallationFence || v.Native.Platform != r.writer.Platform || !lifecycleRevisionWithin(v.ControlRevision, r.index.ControlRevision) {
			return false
		}
		settled = v.SettledMS
	case action >= WritableCandidateStart && action <= WritableCandidateReconcile:
		v, e := journal.ParseWritableCandidateRunningReceipt(body)
		if e != nil || v.Claim.EffectID != r.candidate.EffectID || v.Claim.PayloadSHA256 != r.candidate.PayloadSHA256 || v.Claim.ResourceID != r.writer.ResourceID || v.Claim.WriterReceiptSHA256 != r.candidate.WriterReceiptSHA256 || v.Claim.DataReceiptSHA256 != r.writer.DataReceiptSHA256 || v.Claim.AdmittedFence != r.index.InstallationFence || !lifecycleRevisionWithin(v.Claim.ControlRevision, r.index.ControlRevision) || (v.RecoveryControlRevision != "" && !lifecycleRevisionWithin(v.RecoveryControlRevision, r.index.ControlRevision)) {
			return false
		}
		settled = v.SettledMS
	case action == WritableCandidateProbe:
		v, e := journal.ParseWritableCandidateReadiness(body)
		if e != nil || v.PayloadSHA256 != r.candidate.PayloadSHA256 || v.DataFormat != r.index.Payload.DataFormat {
			return false
		}
		observed, _ := strconv.ParseInt(v.ObservedMS, 10, 64)
		return observed >= started.UnixMilli() && observed <= received.UnixMilli()
	case action == WritableRegistrationDeliver || action == WritableRegistrationRead:
		v, e := journal.ParseWritableBackendRegistrationReceipt(body)
		if e != nil || v.Claim.EffectID != r.registration.EffectID || v.Claim.PayloadSHA256 != r.registration.PayloadSHA256 || v.Claim.SourceSHA256 != r.registration.RunningReceiptSHA256 || v.Claim.DocumentSHA256 != r.registration.DocumentSHA256 || !lifecycleRevisionWithin(v.Claim.ControlRevision, r.index.ControlRevision) || (v.RecoveryControlRevision != "" && !lifecycleRevisionWithin(v.RecoveryControlRevision, r.index.ControlRevision)) {
			return false
		}
		settled = v.SettledMS
	default:
		return false
	}
	n, e := strconv.ParseInt(settled, 10, 64)
	return e == nil && n <= received.UnixMilli()
}

func lifecycleRevisionWithin(value, current string) bool {
	v, e := strconv.ParseInt(value, 10, 64)
	c, ce := strconv.ParseInt(current, 10, 64)
	return e == nil && ce == nil && v > 0 && v <= c
}
