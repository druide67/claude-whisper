// Package warn writes and clears fail-loud sentinels under state/warnings/.
// bash had three divergent copies of this (one had lost the atomic .tmp+mv);
// here there is one atomic writer and one naming rule.
package warn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/druide67/claude-whisper/internal/store"
)

// Sentinel is the JSON body of a .warn file. Extra context goes in Fields.
type Sentinel struct {
	TS   string
	Kind string
	// Fields are merged into the top-level object (e.g. cwd, msg_id, peer).
	Fields map[string]any
}

func path(p store.Paths, kind, key string) string {
	name := kind + ".warn"
	if key != "" {
		name = kind + "-" + key + ".warn"
	}
	return filepath.Join(p.Warnings(), name)
}

// Write creates state/warnings/<kind>-<key>.warn atomically (0600). key is a
// stable suffix (a sha-cwd, a msg-id, …) so a repeating condition rewrites one
// file instead of flooding the directory. Best-effort: returns an error but
// callers typically ignore it (a warning must never crash the caller).
func Write(p store.Paths, kind, key string, fields map[string]any) error {
	obj := map[string]any{
		"ts":   time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"kind": kind,
	}
	for k, v := range fields {
		obj[k] = v
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return store.AtomicWrite(path(p, kind, key), append(b, '\n'))
}

// Clear removes a sentinel once its condition is resolved. No-op if absent.
func Clear(p store.Paths, kind, key string) {
	os.Remove(path(p, kind, key))
}
