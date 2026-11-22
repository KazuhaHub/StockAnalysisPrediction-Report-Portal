package appplatform

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxPublisherNameCharacters bounds the self-declared publisher display name.
const MaxPublisherNameCharacters = 64

// validPublisherName checks an optional name: display text that is already NFC
// with no doubled spaces. Trust never depends on it; the publisher ID does.
func validPublisherName(value *string) bool {
	return value == nil || validDisplayText(*value, MaxPublisherNameCharacters) && norm.NFC.IsNormalString(*value) && !strings.Contains(*value, "  ")
}

// Display text is rendered as plain text by the host, never HTML or a resource
// URL. Locale coverage and built-in icons are deliberately closed.
func validAppDisplay(display AppDisplay) bool {
	if !descriptorDisplayIcons[display.Icon] || len(display.Texts) != len(descriptorLocaleNames) {
		return false
	}
	if display.IconPNG != nil {
		if err := ValidateIcon(*display.IconPNG); err != nil {
			return false
		}
	}
	seen := map[string]bool{}
	for _, text := range display.Texts {
		if !descriptorLocaleNames[text.Locale] || seen[text.Locale] || !validDisplayText(text.Name, MaxAppNameCharacters) || !validDisplayText(text.Description, MaxAppDescriptionCharacters) {
			return false
		}
		seen[text.Locale] = true
	}
	return true
}
func validDisplayText(value string, max int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > max || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			return false
		}
	}
	return true
}
