package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/druide67/claude-whisper/internal/msg"
	"github.com/druide67/claude-whisper/internal/peerid"
	"github.com/druide67/claude-whisper/internal/store"
)

// Rehome implements: whisper rehome <wrong-peer> <correct-peer> [--yes].
// Moves every message from inbox/<wrong> to inbox/<correct>, rewriting each
// message's `to` field, then removes the emptied source dir. The target must
// be a registered peer (else you'd just recreate an orphan).
func Rehome(args []string) int {
	yes := false
	var pos []string
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
		} else {
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return errf(1, "Usage: whisper rehome <wrong-peer> <correct-peer> [--yes]")
	}
	wrong, correct := pos[0], pos[1]
	if !peerid.Valid(wrong) || !peerid.Valid(correct) {
		return errf(1, "Error: peer-ids must be alphanumeric + dash.")
	}
	if wrong == correct {
		return errf(1, "Error: source and target are the same peer.")
	}

	p := store.New()
	pf, err := p.ReadPeers()
	if err != nil {
		return errf(1, "Error: read peers.json: %v", err)
	}
	if !pf.IsRegistered(correct) {
		return errf(1, "Error: target peer %q is not registered (whisper init it first).", correct)
	}

	srcDir := p.Inbox(wrong)
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return errf(1, "Error: no inbox for %q (%v).", wrong, err)
	}
	var msgs []string
	for _, e := range entries {
		if isMsg(e.Name()) {
			msgs = append(msgs, e.Name())
		}
	}
	if len(msgs) == 0 {
		return errf(1, "Nothing to rehome: inbox/%s has no messages.", wrong)
	}

	if !yes {
		fmt.Printf("About to move %d message(s) from inbox/%s → inbox/%s (rewriting .to). Re-run with --yes.\n", len(msgs), wrong, correct)
		return 0
	}

	moved := 0
	for _, name := range msgs {
		b, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			continue
		}
		m, err := msg.Parse(b)
		if err != nil {
			continue
		}
		m.To = correct
		out, err := msg.Marshal(m)
		if err != nil {
			continue
		}
		if store.AtomicWrite(filepath.Join(p.Inbox(correct), name), out) == nil {
			_ = os.Remove(filepath.Join(srcDir, name))
			moved++
		}
	}
	// remove now-empty source dir (best-effort)
	_ = os.Remove(srcDir)
	fmt.Printf("🏠 whisper rehome: moved %d message(s) inbox/%s → inbox/%s.\n", moved, wrong, correct)
	return 0
}
