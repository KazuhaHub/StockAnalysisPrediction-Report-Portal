package appplatform

// ValidCredentialBindingName reports whether name is a credential binding name:
// 1 to 64 characters, a lower-case letter first, then lower-case letters, digits
// and hyphens. It is the same rule on every surface that names a binding (the
// binding review, the foreground gateway and the background broker).
func ValidCredentialBindingName(name string) bool {
	if len(name) < 1 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return false
		}
	}
	return true
}
