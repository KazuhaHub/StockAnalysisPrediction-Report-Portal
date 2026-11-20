package appplatform

import (
	"errors"
	"unicode/utf8"
)

// ErrInvalidPostgresCredential is every refusal of a pg_password credential value. Nothing of the
// input is echoed in it.
var ErrInvalidPostgresCredential = errors.New("invalid PostgreSQL credential")

// PostgresCredential is the value of a pg_password binding: the monitor role and its password. The
// slices are fresh copies the caller owns and wipes; neither value ever exists as a Go string
// inside Portal.
type PostgresCredential struct {
	Username []byte
	Password []byte
}

// Clear wipes both values.
func (c *PostgresCredential) Clear() {
	clear(c.Username)
	clear(c.Password)
	c.Username, c.Password = nil, nil
}

// ValidPostgresUsername is 1..63 bytes of printable ASCII.
func ValidPostgresUsername(v []byte) bool {
	if len(v) < 1 || len(v) > MaxPostgresUsernameLen {
		return false
	}
	for _, b := range v {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}

// ValidPostgresPassword is 1..1024 bytes, none of them NUL. The stored credential holds printable
// ASCII only (see ParsePostgresCredential), a bound the command itself does not need.
func ValidPostgresPassword(v []byte) bool {
	if len(v) < 1 || len(v) > MaxPostgresPasswordLen || !utf8.Valid(v) {
		return false
	}
	for _, b := range v {
		if b == 0 {
			return false
		}
	}
	return true
}

// ParsePostgresCredential reads {"username","password"}. An unknown, duplicate, differently-cased,
// null or non-string member, trailing data or an out-of-bounds value is refused. Like
// ParseACS3Credential it decodes by hand into fresh byte slices, so the password is never a Go
// string and the caller's wipe covers every copy. Both values are printable ASCII.
func ParsePostgresCredential(raw []byte) (PostgresCredential, error) {
	var zero PostgresCredential
	if len(raw) == 0 || len(raw) > PostgresCredentialLimit || !utf8.Valid(raw) {
		return zero, ErrInvalidPostgresCredential
	}
	fields := map[string][]byte{}
	fail := func() (PostgresCredential, error) {
		for _, v := range fields {
			clear(v)
		}
		return zero, ErrInvalidPostgresCredential
	}
	i := 0
	space := func() {
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r') {
			i++
		}
	}
	space()
	if i >= len(raw) || raw[i] != '{' {
		return fail()
	}
	i++
	for first := true; ; first = false {
		space()
		if !first {
			if i >= len(raw) || raw[i] != ',' {
				break
			}
			i++
			space()
		}
		key, next, ok := acs3JSONString(raw, i)
		if !ok {
			return fail()
		}
		i = next
		name := string(key)
		clear(key)
		if _, duplicate := fields[name]; duplicate || (name != "username" && name != "password") {
			return fail()
		}
		space()
		if i >= len(raw) || raw[i] != ':' {
			return fail()
		}
		i++
		space()
		value, next, ok := acs3JSONString(raw, i)
		if !ok {
			return fail()
		}
		fields[name] = value
		i = next
	}
	space()
	if i >= len(raw) || raw[i] != '}' {
		return fail()
	}
	i++
	space()
	if i != len(raw) || len(fields) != 2 || !ValidPostgresUsername(fields["username"]) || !ValidPostgresPassword(fields["password"]) {
		return fail()
	}
	return PostgresCredential{Username: fields["username"], Password: fields["password"]}, nil
}
