package cmd

import (
	"fmt"
	"github.com/druide67/claude-whisper/internal/multi"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/druide67/claude-whisper/internal/store"
)

// List implements: whisper list [--sessions]. Shows each peer with its pending
// inbox count and its cwd (local) or transport type (remote). --sessions adds
// each peer's live sessions with their titles (the addressable identities) —
// anonymous sessions are called out: they cannot be targeted with -s.
func List(args []string) int {
	showSessions := false
	for _, a := range args {
		if a == "--sessions" {
			showSessions = true
		}
	}
	p := store.New()
	pf, err := p.ReadPeers()
	if err != nil {
		return errf(1, "Error: read peers.json: %v", err)
	}
	ids := pf.SortedIDs()
	if len(ids) == 0 {
		fmt.Println("No peers registered. Run whisper init <peer-id>.")
		return 0
	}
	now := time.Now().Unix()
	grace := int64(envInt("WHISPER_SESSION_GRACE", 129600))
	for _, id := range ids {
		e := pf.Peers[id]
		loc := "—"
		if tr, ok := e["transport"].(map[string]any); ok {
			if t, _ := tr["type"].(string); t != "" {
				loc = "transport:" + t
			}
		} else if cwd, _ := e["cwd"].(string); cwd != "" {
			loc = collapseHome(cwd)
		}
		fmt.Printf("%-24s %3d pending   %s\n", id, pendingCount(p, id), loc)
		if showSessions {
			ms := multi.Load(p.MultiState(id))
			titles, anonymous := ms.LiveTitles(now, grace)
			for _, t := range titles {
				fmt.Printf("    └ session %q\n", t)
			}
			if anonymous > 0 {
				fmt.Printf("    └ %d anonymous session(s) — not targetable (rename in the UI to address)\n", anonymous)
			}
		}
	}
	return 0
}

func pendingCount(p store.Paths, peer string) int {
	entries, err := os.ReadDir(p.Inbox(peer))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if isMsg(e.Name()) {
			n++
		}
	}
	return n
}

func collapseHome(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return filepath.Clean(path)
}
