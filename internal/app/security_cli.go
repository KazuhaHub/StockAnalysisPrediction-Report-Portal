package app

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The `security` subcommand: the documented way back in when the enrolment policy has held somebody
// out of their own portal.
//
// Why it has to exist. The mandate can hold an ADMINISTRATOR at the wall — the staff mandate covers
// them, and an account created from the CLI while one is in force is held at the same wall as
// everybody else. The two existing ways back do not reach this: `hashpw` sets a password, which is
// not a second factor, and `adduser` creates an account, which is then subject to the same policy.
// So the escape hatch is a command that can turn the policy off, and it goes through the same
// validation the settings page does, because two implementations of "is this policy satisfiable"
// would eventually disagree about the one state that matters.
//
// It reads and writes the database directly (no HTTP, no session): an operator with shell access to
// the host is the threat model, and they have the database anyway.

// SecurityCommand runs `report-portal security <show|set|clear-mandate> ...`.
func SecurityCommand(cfgPath string, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: report-portal security <show|set <key> <value>|clear-mandate>")
	}
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	os.MkdirAll(config.DirOf(c.DBPath), 0o755)
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return "", err
	}
	defer st.Close()
	s := &Server{st: st}

	switch args[0] {
	case "show":
		return s.securityReport(), nil
	case "set":
		if len(args) != 3 {
			return "", fmt.Errorf("usage: report-portal security set <key> <value>\n%s", securityKeys())
		}
		return s.securitySet(args[1], args[2])
	case "clear-mandate":
		return s.securityClearMandate()
	default:
		return "", fmt.Errorf("unknown security subcommand %q\n%s", args[0], securityKeys())
	}
}

func securityKeys() string {
	return "keys: twofa_totp_enroll, twofa_passkey_enroll, require_2fa_for_staff (true|false)"
}

// securityReport prints the policy as an operator needs to see it: what is allowed, what is required,
// and — for a mandate — how many people it is currently holding out.
func (s *Server) securityReport() string {
	var b strings.Builder
	fmt.Fprintf(&b, "second factors\n")
	fmt.Fprintf(&b, "  authenticator apps   %s\n", onOff(s.switchOn(setTOTPEnroll, true)))
	fmt.Fprintf(&b, "  passkeys             %s\n", onOff(s.switchOn(setPasskeyEnroll, true)))
	fmt.Fprintf(&b, "  password recovery    %s\n", onOff(s.passwordRecoveryEnabled()))
	fmt.Fprintf(&b, "mandates\n")
	fmt.Fprintf(&b, "  staff                %s\n", onOff(s.switchOn(setRequire2FAForStaff, false)))
	groups := s.st.ListUserGroups()
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	for _, g := range groups {
		if !g.Require2FA {
			continue
		}
		n, err := s.st.membersWithoutFactor(g.ID)
		count := "unknown"
		if err == nil {
			count = strconv.Itoa(n)
		}
		fmt.Fprintf(&b, "  %-20s required, %s members without a factor\n", fmt.Sprintf("%s (id %d)", g.Name, g.ID), count)
	}
	return b.String()
}

// securitySet writes one switch, refusing a state nobody could satisfy — the same judgement the
// settings page makes, on the same code.
func (s *Server) securitySet(key, raw string) (string, error) {
	val, err := strconv.ParseBool(strings.TrimSpace(strings.ToLower(raw)))
	if err != nil {
		return "", fmt.Errorf("%s wants true or false, got %q", key, raw)
	}
	proposed := proposedPolicy{}
	switch key {
	case setTOTPEnroll:
		proposed.totp = &val
	case setPasskeyEnroll:
		proposed.passkey = &val
	case setRequire2FAForStaff:
		proposed.staff = &val
	default:
		return "", fmt.Errorf("unknown key %q\n%s", key, securityKeys())
	}
	stuck, err := s.unsatisfiableMandates(proposed)
	if err != nil {
		return "", err
	}
	if len(stuck) > 0 {
		return "", fmt.Errorf("refusing that: it would leave a second factor required with no method "+
			"enabled to provide one, for %s\n%s", strings.Join(stuck, ", "), securityKeys())
	}
	if err := s.st.SetSetting(key, boolSetting(val)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s = %s\n%s", key, onOff(val), s.securityReport()), nil
}

// securityClearMandate turns every mandate off: the staff one and each OU's. It is the break-glass,
// so it refuses nothing and reports exactly what it cleared.
func (s *Server) securityClearMandate() (string, error) {
	var cleared []string
	if err := s.st.SetSetting(setRequire2FAForStaff, boolSetting(false)); err != nil {
		return "", err
	}
	cleared = append(cleared, "staff")
	for _, g := range s.st.ListUserGroups() {
		if !g.Require2FA {
			continue
		}
		if err := s.st.SetGroupRequire2FA(g.ID, false); err != nil {
			return "", err
		}
		cleared = append(cleared, fmt.Sprintf("%s (id %d)", g.Name, g.ID))
	}
	return fmt.Sprintf("mandate cleared for: %s\n%s", strings.Join(cleared, ", "), s.securityReport()), nil
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
