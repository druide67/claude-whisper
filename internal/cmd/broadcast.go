package cmd

import (
	"fmt"
	"strings"

	"github.com/druide67/claude-whisper/internal/peerid"
	"github.com/druide67/claude-whisper/internal/store"
)

// Broadcast implements: whisper broadcast [-t thread] [-f from] [-p normal|urgent] <message>.
// Sends to every registered peer except the sender. Flags -t/-f/-p pass through
// to each send; -r/-F are not meaningful for a broadcast.
func Broadcast(args []string) int {
	var o sendOpts
	rest, err := parseSendFlags(args, &o)
	if err != nil {
		return errf(1, "%v", err)
	}
	if len(rest) < 1 {
		return errf(1, "Usage: whisper broadcast [-t thread] [-f from] [-p normal|urgent] <message>")
	}
	content := strings.Join(rest, " ")
	if content == "" {
		return errf(1, "Error: message cannot be empty.")
	}
	from := o.from
	if from == "" {
		from = readSelfPeer()
	}
	if !peerid.Valid(from) {
		return errf(1, "Error: not initialized. Run whisper init or use --from.")
	}

	p := store.New()
	pf, err := p.ReadPeers()
	if err != nil {
		return errf(1, "Error: read peers.json: %v", err)
	}
	sent := 0
	for _, id := range pf.SortedIDs() {
		if id == from {
			continue
		}
		// broadcast targets are registered by definition, so --force semantics
		// are irrelevant; reuse the single send path for consistency.
		if sendMessage(p, id, from, content, sendOpts{thread: o.thread, from: from, priority: o.priority, force: true}) == 0 {
			sent++
		}
	}
	fmt.Printf("📡 whisper broadcast → %d peer(s)\n", sent)
	return 0
}
