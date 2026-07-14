package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/druide67/claude-whisper/internal/store"
)

// Clean implements: whisper clean [days]. Removes archived messages older than
// `days` days (default 7). A non-numeric arg is tolerated (falls back to 7),
// matching the historical behavior.
func Clean(args []string) int {
	days := 7
	if len(args) > 0 {
		if n, err := strconv.Atoi(args[0]); err == nil && n >= 0 {
			days = n
		}
	}
	p := store.New()
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	entries, err := os.ReadDir(p.Archive())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Nothing to clean (no archive).")
			return 0
		}
		return errf(1, "Error: read archive: %v", err)
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !isMsg(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if os.Remove(filepath.Join(p.Archive(), name)) == nil {
				removed++
			}
		}
	}
	fmt.Printf("🧹 whisper clean: removed %d archived message(s) older than %d day(s).\n", removed, days)
	return 0
}
