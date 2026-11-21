package service

import (
	"encoding/base64"
	"encoding/json"
)

// MarshalEnrollment writes the public enrollment document Portal hands an administrator when it prepares a
// runtime binding (doc 187 section 8.2): a bootstrap document without journal_dir, which host-setup owns. It
// carries only public identity. The result is what ParseEnrollment accepts, and an identity it would refuse
// is refused here.
func MarshalEnrollment(controller Controller, publicKey []byte) ([]byte, error) {
	doc := struct {
		Format              string     `json:"format"`
		Controller          Controller `json:"controller"`
		ControllerPublicKey string     `json:"controller_public_key"`
	}{BootstrapFormat, controller, base64.RawURLEncoding.EncodeToString(publicKey)}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, ErrConfig
	}
	raw = append(raw, '\n')
	if _, err := ParseEnrollment(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
