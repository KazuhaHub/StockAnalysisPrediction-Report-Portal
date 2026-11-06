package driver

type WritableBackendRegistrationSnapshot struct {
	Launch         WritableCandidateLaunchSnapshot `json:"launch"`
	NativeID       string                          `json:"native_id"`
	BindingSHA256  string                          `json:"binding_sha256"`
	DocumentSHA256 string                          `json:"document_sha256"`
	Device, Inode  uint64
	ObservedMS     int64 `json:"observed_ms"`
}

// Only the concrete fixed-file implementation produces this opaque proof. It
// is not a backend handshake, a launch handle or a parsed health response.
type WritableBackendRegistrationObservation struct {
	snapshot WritableBackendRegistrationSnapshot
	valid    bool
}

func (WritableBackendRegistrationObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}
func (o *WritableBackendRegistrationObservation) UnmarshalJSON([]byte) error {
	*o = WritableBackendRegistrationObservation{}
	return ErrContainerUncertain
}
func (o WritableBackendRegistrationObservation) Snapshot() (WritableBackendRegistrationSnapshot, error) {
	s := o.snapshot
	if !o.valid || !validWritableCandidateSnapshot(s.Launch) || !containerSHA.MatchString(s.NativeID) || !containerSHA.MatchString(s.BindingSHA256) || !containerSHA.MatchString(s.DocumentSHA256) || s.Inode == 0 || s.ObservedMS < 0 {
		return WritableBackendRegistrationSnapshot{}, ErrContainerUncertain
	}
	return s, nil
}
