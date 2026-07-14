package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/druide67/claude-whisper/internal/msg"
	"github.com/druide67/claude-whisper/internal/store"
)

type doctorRun struct {
	p                            store.Paths
	fix, yes                     bool
	ok, warns, crits, fixedCount int
	in                           *bufio.Reader
}

// Doctor audits (and optionally fixes) the local whisper state.
func Doctor(args []string) int {
	var fix, yes, listOrphans bool
	for _, a := range args {
		switch a {
		case "--fix":
			fix = true
		case "--yes", "-y":
			yes = true
		case "--list-orphans":
			listOrphans = true
		case "-h", "--help":
			fmt.Println("Usage: whisper doctor [--fix] [--yes] [--list-orphans]")
			return 0
		}
	}
	d := &doctorRun{p: store.New(), fix: fix, yes: yes, in: bufio.NewReader(os.Stdin)}
	if listOrphans {
		return d.listOrphans()
	}
	return d.audit()
}

func (d *doctorRun) emitOK(f string, a ...any)   { d.ok++; fmt.Printf("  ✅ "+f+"\n", a...) }
func (d *doctorRun) emitWarn(f string, a ...any) { d.warns++; fmt.Printf("  ⚠️  "+f+"\n", a...) }
func (d *doctorRun) emitCrit(f string, a ...any) { d.crits++; fmt.Printf("  ❌ "+f+"\n", a...) }
func (d *doctorRun) info(f string, a ...any)     { fmt.Printf("     "+f+"\n", a...) }

// confirm returns true to apply a fix (auto when --yes).
func (d *doctorRun) confirm(prompt string) bool {
	if !d.fix {
		return false
	}
	if d.yes {
		return true
	}
	fmt.Fprintf(os.Stderr, "       Fix: %s? [y/N] ", prompt)
	line, _ := d.in.ReadString('\n')
	switch strings.TrimSpace(line) {
	case "y", "Y", "yes":
		return true
	}
	return false
}

func (d *doctorRun) audit() int {
	fmt.Printf("── whisper doctor — %s ──\n\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"))

	pf, perr := d.p.ReadPeers()
	peersExist := perr == nil && fileExists(d.p.PeersFile())

	// [1] .whisper-peer per registered LOCAL peer (transport peers have no cwd
	// and are skipped — not flagged, unlike the old "no cwd" warning).
	fmt.Println("[1] .whisper-peer files (per registered peer)")
	if !peersExist {
		d.emitCrit("No peers.json at %s — run whisper init first.", d.p.PeersFile())
	} else {
		for _, id := range pf.SortedIDs() {
			e := pf.Peers[id]
			if _, isTransport := e["transport"]; isTransport {
				d.emitOK("%s : transport peer (no local project)", id)
				continue
			}
			cwd, _ := e["cwd"].(string)
			if cwd == "" {
				d.emitWarn("%s : no cwd recorded", id)
				continue
			}
			if !dirExists(cwd) {
				d.emitWarn("%s : cwd %s no longer exists", id, cwd)
				continue
			}
			peerFile := filepath.Join(cwd, ".whisper-peer")
			b, err := os.ReadFile(peerFile)
			switch {
			case err != nil:
				d.emitCrit("%s : .whisper-peer MISSING at %s", id, cwd)
				if d.confirm(fmt.Sprintf("create %s", peerFile)) {
					d.applyWritePeer(cwd, id)
				}
			case strings.TrimSpace(string(b)) != id:
				d.emitCrit("%s : .whisper-peer content mismatch (got %q)", id, strings.TrimSpace(string(b)))
				if d.confirm(fmt.Sprintf("rewrite %s", peerFile)) {
					d.applyWritePeer(cwd, id)
				}
			default:
				d.emitOK("%s : .whisper-peer present", id)
			}
		}
	}

	// [1b] orphan inbox dirs (unknown peer-id).
	fmt.Println("\n[1b] orphan inbox directories (unknown peer-id)")
	orphans := 0
	for _, name := range d.inboxDirs() {
		if peersExist && pf.IsRegistered(name) {
			continue
		}
		orphans++
		if n := pendingCount(d.p, name); n > 0 {
			d.emitCrit("inbox/%s : unknown peer-id with %d pending message(s) — likely mis-addressed", name, n)
			d.info("review: ls %s — re-send to the correct peer-id or whisper rehome", d.p.Inbox(name))
		} else {
			d.emitWarn("inbox/%s : unknown peer-id (empty) — leftover dir", name)
		}
	}
	if orphans == 0 {
		d.emitOK("no orphan inbox directories")
	}

	// [2] root perms
	fmt.Println("\n[2] ~/.claude-whisper/ perms")
	d.checkPerm(d.p.Root, "700", true)

	// [3] inbox/<peer> perms
	fmt.Println("\n[3] inbox/<peer>/ perms (700 expected)")
	for _, name := range d.inboxDirs() {
		d.checkPerm(d.p.Inbox(name), "700", false)
	}

	// [4] message file perms
	fmt.Println("\n[4] message file perms (600 expected)")
	bad := 0
	for _, dir := range append(d.inboxPaths(), d.p.Archive(), filepath.Join(d.p.Root, "run", "processing")) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !isMsg(e.Name()) {
				continue
			}
			f := filepath.Join(dir, e.Name())
			if permOf(f) != "600" {
				bad++
				d.emitWarn("%s perms = %s", f, permOf(f))
				if d.confirm("chmod 600 " + e.Name()) {
					if os.Chmod(f, 0o600) == nil {
						d.fixedCount++
						d.info("→ fixed")
					}
				}
			}
		}
	}
	if bad == 0 {
		d.emitOK("all message files at 600")
	}

	// [5] stuck in run/processing
	fmt.Println("\n[5] run/processing/ stuck messages (mtime > 1h)")
	stuck := 0
	if entries, err := os.ReadDir(filepath.Join(d.p.Root, "run", "processing")); err == nil {
		cutoff := time.Now().Add(-time.Hour)
		for _, e := range entries {
			if !isMsg(e.Name()) {
				continue
			}
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				stuck++
			}
		}
	}
	if stuck == 0 {
		d.emitOK("no stuck messages")
	} else {
		d.emitWarn("%d message(s) stuck > 1h in run/processing/ — operator review", stuck)
	}

	// [6] hook in settings.json
	fmt.Println("\n[6] hook registration in ~/.claude/settings.json")
	d.checkHook()

	// [8] sentinels
	fmt.Println("\n[8] state/warnings/ sentinels (informational)")
	if warns := d.warnFiles(); len(warns) == 0 {
		d.emitOK("no sentinel warnings")
	} else {
		d.emitWarn("%d sentinel(s) present:", len(warns))
		for _, w := range warns {
			d.info("%s", filepath.Base(w))
		}
	}

	// [9] stale last_seen
	staleDays := envInt("WHISPER_STALE_DAYS", 7)
	fmt.Printf("\n[9] peers.json last_seen (stale > %dd)\n", staleDays)
	stale := 0
	if peersExist {
		cutoff := time.Now().Add(-time.Duration(staleDays) * 24 * time.Hour)
		for _, id := range pf.SortedIDs() {
			e := pf.Peers[id]
			if _, isTransport := e["transport"]; isTransport {
				continue
			}
			cwd, _ := e["cwd"].(string)
			if cwd == "" || !dirExists(cwd) {
				continue
			}
			ls, _ := e["last_seen"].(string)
			t, err := time.Parse("2006-01-02T15:04:05Z", ls)
			if err != nil {
				continue
			}
			if t.Before(cutoff) {
				stale++
				d.emitWarn("%s : last_seen=%s (%dd ago)", id, ls, int(time.Since(t).Hours()/24))
			}
		}
	}
	if stale == 0 {
		d.emitOK("all active peers fresh")
	}

	// summary
	fmt.Printf("\n── summary ──\n  ✅ ok       : %d\n  ⚠️  warnings : %d\n  ❌ critical : %d\n", d.ok, d.warns, d.crits)
	if d.fix {
		fmt.Printf("  🔧 fixed    : %d\n", d.fixedCount)
	}
	if d.crits > 0 && d.fixedCount < d.crits {
		return 1
	}
	return 0
}

func (d *doctorRun) listOrphans() int {
	fmt.Printf("── undelivered / orphan messages — %s ──\n\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	found := 0
	pf, err := d.p.ReadPeers()
	if err == nil {
		for _, name := range d.inboxDirs() {
			if pf.IsRegistered(name) {
				continue
			}
			entries, _ := os.ReadDir(d.p.Inbox(name))
			for _, e := range entries {
				if !isMsg(e.Name()) {
					continue
				}
				found++
				fmt.Printf("  [orphan-inbox: %s] (unknown peer-id — nobody reads this)\n", name)
				if b, err := os.ReadFile(filepath.Join(d.p.Inbox(name), e.Name())); err == nil {
					if m, err := msg.Parse(b); err == nil {
						prev := truncRunes(strings.Join(strings.Fields(m.Content), " "), 200)
						fmt.Printf("    id=%s  from=%s  to=%s  ts=%s\n    preview: %s\n\n", m.ID, m.From, m.To, m.Timestamp, prev)
						continue
					}
				}
				fmt.Printf("    %s\n\n", e.Name())
			}
		}
	}
	// Any sentinel is something undelivered/wrong — list them all (not just the
	// two kinds the bash globbed, so a new kind is never silently missed).
	for _, w := range d.warnFiles() {
		found++
		fmt.Printf("  [sentinel: %s]\n", filepath.Base(w))
		if b, err := os.ReadFile(w); err == nil {
			fmt.Printf("    %s\n", strings.TrimSpace(string(b)))
		}
		fmt.Println()
	}
	if found == 0 {
		fmt.Println("  ✅ no orphan or undelivered messages")
	} else {
		fmt.Println("  → re-route a mis-addressed inbox with: whisper rehome <wrong-peer> <correct-peer>")
	}
	return 0
}

// --- doctor helpers ---------------------------------------------------------

// applyWritePeer mirrors init's .whisper-peer write (same 0644 — it lives in
// the project dir, is not a secret, and must stay readable by project tooling).
func (d *doctorRun) applyWritePeer(cwd, id string) {
	if os.WriteFile(filepath.Join(cwd, ".whisper-peer"), []byte(id+"\n"), 0o644) == nil {
		d.fixedCount++
		d.info("→ fixed")
	}
}

func (d *doctorRun) checkPerm(path, want string, critIfMissing bool) {
	if !dirExists(path) {
		if critIfMissing {
			d.emitCrit("%s not found", path)
		}
		return
	}
	if permOf(path) == want {
		d.emitOK("%s perms = %s", path, want)
		return
	}
	d.emitWarn("%s perms = %s (expected %s)", path, permOf(path), want)
	if d.confirm("chmod " + want + " " + path) {
		mode := os.FileMode(0o700)
		if want == "600" {
			mode = 0o600
		}
		if os.Chmod(path, mode) == nil {
			d.fixedCount++
			d.info("→ fixed")
		}
	}
}

func (d *doctorRun) checkHook() {
	home, _ := os.UserHomeDir()
	if d.p.Root != filepath.Join(home, ".claude-whisper") {
		d.emitOK("skipped (custom WHISPER_DIR)")
		return
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	b, err := os.ReadFile(settings)
	if err != nil {
		d.emitWarn("%s not found", settings)
		return
	}
	// Parse the actual hook entries instead of substring-matching the whole
	// file: "check-inbox" appearing in an unrelated key (env var, permission
	// rule, another hook's comment) must not pass for an installed hook.
	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &root) == nil {
		for _, entry := range root.Hooks["UserPromptSubmit"] {
			for _, h := range entry.Hooks {
				if strings.Contains(h.Command, "check-inbox") {
					d.emitOK("hook installed: %s", h.Command)
					return
				}
			}
		}
	}
	d.emitCrit("hook missing in %s", settings)
	if d.confirm("install UserPromptSubmit hook") {
		if installHook(d.p) == "installed" {
			d.fixedCount++
			d.info("→ fixed")
		}
	}
}

func (d *doctorRun) inboxDirs() []string {
	var out []string
	entries, err := os.ReadDir(filepath.Join(d.p.Root, "inbox"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func (d *doctorRun) inboxPaths() []string {
	var out []string
	for _, name := range d.inboxDirs() {
		out = append(out, d.p.Inbox(name))
	}
	return out
}

func (d *doctorRun) warnFiles() []string {
	var out []string
	entries, err := os.ReadDir(d.p.Warnings())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".warn") {
			out = append(out, filepath.Join(d.p.Warnings(), e.Name()))
		}
	}
	return out
}

func permOf(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "???"
	}
	return fmt.Sprintf("%o", fi.Mode().Perm())
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
