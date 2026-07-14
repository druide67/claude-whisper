// Package cmd holds one function per whisper subcommand. Each returns a process
// exit code. Shared filesystem/state logic lives in internal/store.
package cmd

import (
	"fmt"
	"github.com/druide67/claude-whisper/internal/store"
	"os"
	"path/filepath"
	"strings"

	"github.com/druide67/claude-whisper/internal/peerid"
)

// readSelfPeer returns the peer-id from ./.whisper-peer, or "" if absent/invalid.
func readSelfPeer() string {
	b, err := os.ReadFile(".whisper-peer")
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if !peerid.Valid(id) {
		return ""
	}
	return id
}

// truncRunes returns s truncated to at most n runes. UTF-8 safe: a byte-slice
// s[:n] could cut a multibyte character in half and emit invalid UTF-8 into
// the hook output or a sentinel.
func truncRunes(s string, n int) string {
	if len(s) <= n { // bytes ≤ n ⇒ runes ≤ n
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// inFlight reports whether a message basename is currently being delivered —
// claimed by a session (run/claims/<sid>/) or staged by the extension leader
// (run/processing/).
func inFlight(p store.Paths, base string) bool {
	if fileExists(filepath.Join(p.Root, "run", "processing", base)) {
		return true
	}
	claims := filepath.Join(p.Root, "run", "claims")
	entries, err := os.ReadDir(claims)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(claims, e.Name(), base)) {
			return true
		}
	}
	return false
}

// errf prints to stderr (newline appended) and returns code, for one-line
// `return errf(1, "...")` at call sites.
func errf(code int, format string, a ...any) int {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	fmt.Fprintf(os.Stderr, format, a...)
	return code
}
