package peerid

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Broadcast is the reserved session target meaning "every live session".
const Broadcast = "*"

// maxTitleRunes bounds a session-title address. Titles become map keys and
// rendered banner content; an unbounded one is abuse.
const maxTitleRunes = 64

// NormalizeTitle canonicalizes a session title used as an address: trims
// surrounding whitespace and applies Unicode NFC. macOS UIs may hand out NFD
// while a keyboard types NFC — two visually identical titles must compare
// equal, or a target silently never matches.
func NormalizeTitle(s string) string {
	return norm.NFC.String(strings.TrimSpace(s))
}

// ValidateTitle checks a normalized title against the address grammar:
// non-empty, ≤ 64 runes, no control characters (a title is rendered inside
// hook banners read by an LLM — a newline is an injection vector, not a
// name), and never the reserved broadcast token.
func ValidateTitle(s string) error {
	if s == "" {
		return fmt.Errorf("empty title")
	}
	if s == Broadcast {
		return fmt.Errorf("%q is reserved for broadcast", Broadcast)
	}
	if utf8.RuneCountInString(s) > maxTitleRunes {
		return fmt.Errorf("title exceeds %d characters", maxTitleRunes)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("title contains control characters")
		}
	}
	return nil
}
