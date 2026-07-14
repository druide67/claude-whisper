package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/druide67/claude-whisper/internal/peerid"
	"github.com/druide67/claude-whisper/internal/store"
)

var transportTypeRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Init implements:
//
//	whisper init <peer-id> [project-dir]
//	whisper init <peer-id> --transport <type> [--ssh-alias X] [--key-path Y]
//
// Local mode registers a project peer (peers.json + inbox + .whisper-peer),
// installs the UserPromptSubmit hook in ~/.claude/settings.json and links the
// binary onto PATH. Transport mode registers a remote peer (pointer only, no
// cwd, no hook) whose inbox is drained by `whisper spool-server` over SSH.
func Init(args []string) int {
	const usage = "Usage: whisper init <peer-id> [project-dir] | <peer-id> --transport <type> [--ssh-alias X] [--key-path Y]"
	if len(args) == 0 {
		return errf(1, usage)
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Println(usage)
		return 0
	}
	peer := args[0]
	if !peerid.Valid(peer) {
		return errf(1, "Error: peer-id must start with a letter or digit and contain only letters, digits, and dashes (got: %s)", peer)
	}

	var transportType, sshAlias, keyPath, projectDir string
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--transport":
			if i+1 >= len(rest) || isFlag(rest[i+1]) {
				return errf(1, "Error: --transport needs a type")
			}
			transportType = rest[i+1]
			i++
		case "--ssh-alias":
			if i+1 >= len(rest) || isFlag(rest[i+1]) {
				return errf(1, "Error: --ssh-alias needs a value")
			}
			sshAlias = rest[i+1]
			i++
		case "--key-path":
			if i+1 >= len(rest) || isFlag(rest[i+1]) {
				return errf(1, "Error: --key-path needs a value")
			}
			keyPath = rest[i+1]
			i++
		default:
			if projectDir == "" && !isFlag(rest[i]) {
				projectDir = rest[i]
			}
		}
	}

	p := store.New()
	now := time.Now()

	if transportType != "" {
		if !transportTypeRe.MatchString(transportType) {
			return errf(1, "Error: --transport type must be lowercase alphanumeric/dash (got: %s)", transportType)
		}
		if err := store.EnsureDir(p.Inbox(peer)); err != nil {
			return errf(1, "Error: create inbox: %v", err)
		}
		if err := p.UpdatePeers(func(pf *store.PeersFile) {
			pf.RegisterTransport(peer, transportType, sshAlias, keyPath, now)
		}); err != nil {
			return errf(1, "Error: write peers.json: %v", err)
		}
		fmt.Printf("📡 Transport peer registered: %s (transport=%s)\n", peer, transportType)
		fmt.Printf("   Inbox: %s\n", p.Inbox(peer))
		fmt.Println("   Remote peer: no .whisper-peer, no hook. Drained by `whisper spool-server` over SSH.")
		return 0
	}

	// --- local project peer ---
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}
	if err := store.EnsureDir(p.Inbox(peer)); err != nil {
		return errf(1, "Error: create inbox: %v", err)
	}
	if err := p.UpdatePeers(func(pf *store.PeersFile) {
		pf.RegisterLocal(peer, projectDir, now)
	}); err != nil {
		return errf(1, "Error: write peers.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".whisper-peer"), []byte(peer+"\n"), 0o644); err != nil {
		return errf(1, "Error: write .whisper-peer: %v", err)
	}

	hookStatus := installHook(p)

	fmt.Printf("📡 Whisper initialized: %s\n", peer)
	fmt.Printf("   Project: %s\n", collapseHome(projectDir))
	fmt.Printf("   Hook: %s\n", hookStatus)
	return 0
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

// installHook upserts the UserPromptSubmit hook into ~/.claude/settings.json,
// pointing at this binary's `check-inbox` subcommand. Skipped when WHISPER_DIR
// is overridden (sandbox/tests) so we never touch the real settings there.
func installHook(p store.Paths) string {
	home, _ := os.UserHomeDir()
	if p.Root != filepath.Join(home, ".claude-whisper") {
		return "skipped (custom WHISPER_DIR)"
	}
	bin, err := os.Executable()
	if err != nil {
		return "skipped (cannot resolve binary path)"
	}
	cmdStr := bin + " check-inbox"
	settings := filepath.Join(home, ".claude", "settings.json")

	root := map[string]any{}
	if b, err := os.ReadFile(settings); err == nil {
		// NEVER clobber a settings.json we cannot parse: a stray trailing comma
		// or JSONC would otherwise be rewritten away, destroying the user's
		// model/env/permissions/other hooks. Abort instead.
		if strings.TrimSpace(string(b)) != "" {
			if err := json.Unmarshal(b, &root); err != nil {
				return "skipped (settings.json unparseable — fix it, then re-run)"
			}
		}
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	ups, _ := hooks["UserPromptSubmit"].([]any)

	// already installed?
	for _, entry := range ups {
		em, _ := entry.(map[string]any)
		hs, _ := em["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if hm["command"] == cmdStr {
				return "already configured"
			}
		}
	}
	ups = append(ups, map[string]any{
		"matcher": "",
		"hooks":   []any{map[string]any{"type": "command", "command": cmdStr}},
	})
	hooks["UserPromptSubmit"] = ups
	root["hooks"] = hooks

	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "skipped (marshal error)"
	}
	if os.MkdirAll(filepath.Dir(settings), 0o755) != nil {
		return "skipped (mkdir)"
	}
	tmp := settings + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o644) != nil {
		return "skipped (write)"
	}
	if os.Rename(tmp, settings) != nil {
		os.Remove(tmp)
		return "skipped (rename)"
	}
	return "installed"
}
