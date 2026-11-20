package app

import (
	"regexp"
	"strings"
	"time"
)

// Announcement variables: placeholders an operator writes into a title or body, rendered by the
// reader's browser because only the browser knows the reader's clock and zone. The server never
// renders them; it only refuses a save whose text holds a placeholder the reader could not render,
// so a typo is caught by the operator who made it instead of being shown raw to every reader.
//
// Grammar (mirrored exactly by web/src/lib/announcementVars.ts — change both together):
//
//	{{ name [":" arg] *("|" option) }}
//	now        no arg; options: one format (date|time|datetime), one IANA zone
//	time       arg = an RFC3339 instant; options as for now
//	countdown  arg = an RFC3339 instant; no options
//	site       no arg, no options
//
// A zone is always a full IANA name ("Asia/Shanghai"). Abbreviations are refused on purpose:
// "CST" is US Central in the zone database and China Standard Time in common usage, and a
// maintenance notice off by fourteen hours is worse than one with no zone at all.

var (
	announcementVarToken = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
	// Seconds and fractions are optional; the offset is not. A zoneless time is exactly the
	// ambiguity the variable exists to remove.
	// The year is four digits from 1000: below that the browser's Date.UTC reads 0-99 as 1900+y
	// and a padded rendering would need special-casing, for no announcement anybody writes.
	// The offset is bounded explicitly because time.Parse accepts +24:00 and +12:60, which the
	// browser's Date.parse refuses — a saved token every reader would see raw.
	announcementVarInstant = regexp.MustCompile(`^[1-9]\d{3}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,9})?)?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)$`)
	// The canonical spelling only. LoadLocation alone is not enough: on a case-insensitive file
	// system it accepts "asia/shanghai", and the browser renders the label exactly as written.
	announcementVarZone = regexp.MustCompile(`^(UTC|[A-Z][A-Za-z_-]*(/[A-Z0-9][A-Za-z0-9_+-]*)+)$`)
)

var announcementVarFormats = map[string]bool{"date": true, "time": true, "datetime": true}

// checkAnnouncementVars returns the first placeholder in s that is not a valid variable, verbatim,
// or "" when every placeholder is valid. Text without "{{...}}" is always valid.
func checkAnnouncementVars(s string) string {
	for _, m := range announcementVarToken.FindAllStringSubmatch(s, -1) {
		if !validAnnouncementVar(m[1]) {
			return m[0]
		}
	}
	return ""
}

func validAnnouncementVar(body string) bool {
	parts := strings.Split(body, "|")
	head := trimVarSpace(parts[0])
	opts := parts[1:]
	name, arg, hasArg := strings.Cut(head, ":")
	name, arg = trimVarSpace(name), trimVarSpace(arg)
	switch name {
	case "site":
		return !hasArg && len(opts) == 0
	case "countdown":
		return hasArg && validAnnouncementInstant(arg) && len(opts) == 0
	case "now":
		return !hasArg && validAnnouncementVarOptions(opts)
	case "time":
		return hasArg && validAnnouncementInstant(arg) && validAnnouncementVarOptions(opts)
	}
	return false
}

// trimVarSpace trims ASCII spaces and tabs only. strings.TrimSpace and JavaScript's trim disagree
// on Unicode spaces (U+0085, U+FEFF), and a disagreement here is a token the server saves and the
// reader cannot render.
func trimVarSpace(s string) string {
	return strings.Trim(s, " \t")
}

func validAnnouncementInstant(s string) bool {
	if !announcementVarInstant.MatchString(s) {
		return false
	}
	layout := "2006-01-02T15:04Z07:00"
	if len(s) > 16 && s[16] == ':' {
		layout = time.RFC3339Nano
	}
	_, err := time.Parse(layout, s)
	return err == nil
}

func validAnnouncementVarOptions(opts []string) bool {
	format, zone := false, false
	for _, o := range opts {
		o = trimVarSpace(o)
		switch {
		case announcementVarFormats[o]:
			if format {
				return false
			}
			format = true
		case announcementVarZone.MatchString(o):
			if zone {
				return false
			}
			if _, err := time.LoadLocation(o); err != nil {
				return false
			}
			zone = true
		default:
			return false
		}
	}
	return true
}
