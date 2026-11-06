package driver

import "errors"

var ErrDataWriterUncertain = errors.New("data writer access requires reconciliation")

const DataWriterSandboxProfile = "linux-docker-rootful-projectquota-writer/v1-dev"

func DataWriterPolicySHA256() string {
	if candidateSeccompError != nil {
		return ""
	}
	return containerHash([]byte(candidateSeccompOption))
}

// DataWriterBinding is supplied only by the verified, durably claimed journal
// attempt. Shape validation in the native driver is not signature verification.
type DataWriterBinding struct {
	EffectID, ClaimID, PayloadSHA256, DataReceiptSHA256 string
}
type DataWriterRequest struct {
	Binding    DataWriterBinding
	Identity   CandidateIdentity
	Descriptor []byte
	Platform   string
	Data       CandidateDataEvidence
}
type DataWriterSnapshot struct {
	Binding                DataWriterBinding
	Identity               CandidateIdentity
	Data                   CandidateDataEvidence
	ImageID, Platform      string
	Profile, SeccompSHA256 string
	UsedBytes, UsedInodes  int64
	ObservedMS             int64
}

// DataWriterObservation is produced only by the concrete native transition.
// A decoded snapshot, callback boolean or copied JSON cannot construct it.
type DataWriterObservation struct {
	snapshot DataWriterSnapshot
	valid    bool
}

func (DataWriterObservation) MarshalJSON() ([]byte, error) { return nil, ErrDataWriterUncertain }
func (o *DataWriterObservation) UnmarshalJSON([]byte) error {
	*o = DataWriterObservation{}
	return ErrDataWriterUncertain
}
func (o DataWriterObservation) Snapshot() (DataWriterSnapshot, error) {
	return dataWriterSnapshot(o.snapshot, o.valid)
}

// DataWriterRecoveryObservation is observation-only evidence. It cannot be used
// to settle a live permission execution, or be reconstructed from JSON.
type DataWriterRecoveryObservation struct {
	snapshot DataWriterSnapshot
	valid    bool
}

func (DataWriterRecoveryObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrDataWriterUncertain
}
func (o *DataWriterRecoveryObservation) UnmarshalJSON([]byte) error {
	*o = DataWriterRecoveryObservation{}
	return ErrDataWriterUncertain
}
func (o DataWriterRecoveryObservation) Snapshot() (DataWriterSnapshot, error) {
	return dataWriterSnapshot(o.snapshot, o.valid)
}
func dataWriterSnapshot(s DataWriterSnapshot, valid bool) (DataWriterSnapshot, error) {
	if !valid || !validDataWriterBinding(s.Binding) || !validContainerOwner(candidateOwner(s.Identity)) || s.Profile != DataWriterSandboxProfile || candidateSeccompError != nil || s.SeccompSHA256 != containerHash([]byte(candidateSeccompOption)) || !nativeImageID.MatchString(s.ImageID) || (s.Platform != "linux/amd64" && s.Platform != "linux/arm64") || s.Data.NamespaceInode == 0 || s.Data.ProjectID == 0 || s.Data.Root.Inode == 0 || !containerUUID.MatchString(s.Data.Root.FilesystemUUID) || s.Data.Root.FilesystemUUID == "00000000-0000-0000-0000-000000000000" || s.Data.QuotaBytes <= 0 || s.Data.QuotaBytes%512 != 0 || s.Data.QuotaInodes <= 0 || s.UsedBytes < 0 || s.UsedBytes > s.Data.QuotaBytes || s.UsedInodes < 0 || s.UsedInodes > s.Data.QuotaInodes || s.ObservedMS < 0 {
		return DataWriterSnapshot{}, ErrDataWriterUncertain
	}
	return s, nil
}
func validDataWriterBinding(b DataWriterBinding) bool {
	return containerUUID.MatchString(b.EffectID) && b.EffectID != "00000000-0000-0000-0000-000000000000" && containerUUID.MatchString(b.ClaimID) && b.ClaimID != "00000000-0000-0000-0000-000000000000" && containerSHA.MatchString(b.PayloadSHA256) && containerSHA.MatchString(b.DataReceiptSHA256)
}
