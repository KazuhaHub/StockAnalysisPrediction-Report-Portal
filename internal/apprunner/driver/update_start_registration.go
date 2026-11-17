package driver

type UpdateStartRegistrationSnapshot struct {
	Launch         UpdateStartLaunchSnapshot `json:"launch"`
	NativeID       string                    `json:"native_id"`
	BindingSHA256  string                    `json:"binding_sha256"`
	DocumentSHA256 string                    `json:"document_sha256"`
	Device, Inode  uint64
	ObservedMS     int64 `json:"observed_ms"`
}

// UpdateStartRegistrationObservation is opaque and produced only by the
// concrete protected-file implementation. It is not a handshake, backend
// authorization or active-generation receipt.
type UpdateStartRegistrationObservation struct {
	snapshot UpdateStartRegistrationSnapshot
	valid    bool
}

func (UpdateStartRegistrationObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}

func (o *UpdateStartRegistrationObservation) UnmarshalJSON([]byte) error {
	*o = UpdateStartRegistrationObservation{}
	return ErrContainerUncertain
}

func (o UpdateStartRegistrationObservation) Snapshot() (UpdateStartRegistrationSnapshot, error) {
	s := o.snapshot
	if !o.valid || !validUpdateStartSnapshot(s.Launch) ||
		!containerSHA.MatchString(s.NativeID) ||
		!containerSHA.MatchString(s.BindingSHA256) ||
		!containerSHA.MatchString(s.DocumentSHA256) || s.Inode == 0 ||
		s.ObservedMS < 0 {
		return UpdateStartRegistrationSnapshot{}, ErrContainerUncertain
	}
	return s, nil
}
