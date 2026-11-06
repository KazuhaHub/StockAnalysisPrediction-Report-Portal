package driver

// BackendRegistrationSnapshot records a bounded protected-file observation under
// a running candidate. It does not prove application consumption or handshake.
type BackendRegistrationSnapshot struct {
	Launch         CandidateLaunchSnapshot `json:"launch"`
	NativeID       string                  `json:"native_id"`
	BindingSHA256  string                  `json:"binding_sha256"`
	DocumentSHA256 string                  `json:"document_sha256"`
	Device, Inode  uint64
	ObservedMS     int64 `json:"observed_ms"`
}
type BackendRegistrationObservation struct {
	snapshot BackendRegistrationSnapshot
	valid    bool
}

func (BackendRegistrationObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}
func (o *BackendRegistrationObservation) UnmarshalJSON([]byte) error {
	*o = BackendRegistrationObservation{}
	return ErrContainerUncertain
}
func (o BackendRegistrationObservation) Snapshot() (BackendRegistrationSnapshot, error) {
	if !o.valid || !containerSHA.MatchString(o.snapshot.NativeID) || !containerSHA.MatchString(o.snapshot.BindingSHA256) || !containerSHA.MatchString(o.snapshot.DocumentSHA256) || o.snapshot.Inode == 0 || o.snapshot.ObservedMS < 0 {
		return BackendRegistrationSnapshot{}, ErrContainerUncertain
	}
	return o.snapshot, nil
}
