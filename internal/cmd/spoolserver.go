package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/druide67/claude-whisper/internal/multi"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/druide67/claude-whisper/internal/msg"
	"github.com/druide67/claude-whisper/internal/peerid"
	"github.com/druide67/claude-whisper/internal/store"
	"github.com/druide67/claude-whisper/internal/warn"
)

// SpoolServer is the SSH forced-command transport target. SPOOL-ONLY: it
// deposits/reads/archives JSON under inbox/ + archive/ and NEVER executes
// anything. Trust model:
//   - the peer-id (args[0]) is the trust anchor, frozen by the forced-command
//     in authorized_keys — the remote client cannot change it;
//   - the verb comes from $SSH_ORIGINAL_COMMAND and is strictly whitelisted,
//     never evaluated;
//   - deposit forces message.from == peer (identity bound to the SSH key).
func SpoolServer(args []string) int {
	if len(args) == 0 || !peerid.Valid(args[0]) {
		return errf(1, "spool-server: invalid or missing peer-id")
	}
	peer := args[0]

	// Verb source: SSH_ORIGINAL_COMMAND in production; remaining args as a
	// local/test fallback. Split into tokens WITHOUT executing — we only ever
	// match the first token.
	raw := os.Getenv("SSH_ORIGINAL_COMMAND")
	if raw == "" {
		raw = strings.Join(args[1:], " ")
	}
	tokens := strings.Fields(raw)
	verb, arg := "", ""
	if len(tokens) > 0 {
		verb = tokens[0]
	}
	if len(tokens) > 1 {
		arg = tokens[1]
	}

	p := store.New()
	switch verb {
	case "fetch":
		return spoolFetch(p, peer)
	case "deposit":
		return spoolDeposit(p, peer)
	case "confirm":
		return spoolConfirm(p, peer, arg)
	default:
		return errf(1, "spool-server: unknown verb %q (allowed: fetch, deposit, confirm)", verb)
	}
}

// fetch streams the peer's pending inbox as NDJSON (one compact object per
// line). It does NOT consume. Corrupt files are surfaced via a sentinel rather
// than skipped forever.
func spoolFetch(p store.Paths, peer string) int {
	entries, err := os.ReadDir(p.Inbox(peer))
	if err != nil {
		return 0 // no inbox yet = nothing pending
	}
	out := os.Stdout
	for _, e := range entries {
		if !isMsg(e.Name()) {
			continue
		}
		f := filepath.Join(p.Inbox(peer), e.Name())
		b, err := os.ReadFile(f)
		if err != nil {
			_ = warn.Write(p, "spool-unreadable", shaKey(f), map[string]any{"peer": peer, "file": f})
			continue
		}
		var compact bytes.Buffer
		if json.Compact(&compact, b) != nil {
			_ = warn.Write(p, "spool-unreadable", shaKey(f), map[string]any{"peer": peer, "file": f})
			continue
		}
		compact.WriteByte('\n')
		out.Write(compact.Bytes())
	}
	return 0
}

// deposit reads a bounded whisper JSON on stdin, validates it (from-force,
// registered recipient, well-formed id, content size), and writes it — never
// overwriting an existing id.
func spoolDeposit(p store.Paths, peer string) int {
	maxContent := envInt("WHISPER_MAX_CONTENT_BYTES", 16384)
	maxPayload := envInt("WHISPER_MAX_PAYLOAD_BYTES", maxContent+8192)

	// Bound the client-controlled stdin (DoS guard): read one byte past the
	// limit so we can detect an oversize payload.
	payload, _ := io.ReadAll(io.LimitReader(os.Stdin, int64(maxPayload)+1))
	if len(payload) > maxPayload {
		return errf(1, "spool-server: payload too large (> %d bytes) — refused", maxPayload)
	}
	var m struct {
		From, To, ID, Content string
	}
	// tolerate arbitrary extra fields; only these four are validated
	var probe map[string]any
	if json.Unmarshal(payload, &probe) != nil {
		return errf(1, "spool-server: invalid JSON payload")
	}
	m.From, _ = probe["from"].(string)
	m.To, _ = probe["to"].(string)
	m.ID, _ = probe["id"].(string)
	m.Content, _ = probe["content"].(string)

	// from-force: identity is bound to the SSH key, never free.
	if m.From != peer {
		return errf(1, "spool-server: from %q != %q — rejected (identity spoof)", m.From, peer)
	}
	if !peerid.Valid(m.To) {
		return errf(1, "spool-server: invalid 'to'")
	}
	// fail-closed: without the registry we cannot validate 'to'.
	if _, err := os.Stat(p.PeersFile()); err != nil {
		return errf(1, "spool-server: peers.json missing — cannot validate 'to' — refused")
	}
	pf, err := p.ReadPeers()
	if err != nil || !pf.IsRegistered(m.To) {
		return errf(1, "spool-server: unknown peer 'to=%s'", m.To)
	}
	if !peerid.ValidMsgID(m.ID) {
		return errf(1, "spool-server: invalid message id")
	}
	// Anti-DoS backlog cap: a remote key may only queue a bounded number of
	// pending messages per recipient — past that, deposits are refused until
	// the recipient drains its inbox. Loud on both sides: exit 1 for the
	// remote client, a sentinel for the local operator.
	maxPending := envInt("WHISPER_SPOOL_MAX_PENDING", 200)
	if n := pendingCount(p, m.To); n >= maxPending {
		_ = warn.Write(p, "spool-inbox-full", m.To, map[string]any{
			"peer": peer, "to": m.To, "pending": n, "max": maxPending,
		})
		return errf(1, "spool-server: inbox/%s has %d pending (max %d) — deposit refused", m.To, n, maxPending)
	}
	if len([]byte(m.Content)) > maxContent {
		return errf(1, "spool-server: content too large (%d > %d bytes)", len([]byte(m.Content)), maxContent)
	}
	// Accept only what check-inbox can later parse: a wrongly-typed field
	// (e.g. ttl:"x") would otherwise be deposited but silently skipped forever
	// by the hook's msg.Parse — stuck on the transport path with no sentinel.
	parsed, err := msg.Parse(payload)
	if err != nil {
		return errf(1, "spool-server: payload not a well-formed whisper message — refused")
	}
	// The session target arrives from the NETWORK: validate the address
	// grammar (a crafted title is a conversation-sniping / banner-injection /
	// escalation-spam vector).
	if parsed.Session != "" && parsed.Session != peerid.Broadcast {
		if err := peerid.ValidateTitle(peerid.NormalizeTitle(parsed.Session)); err != nil {
			return errf(1, "spool-server: invalid session target: %v", err)
		}
	}

	// Uniqueness across the WHOLE message lifecycle, not just the inbox: an id
	// in run/claims/** or run/processing/ is being delivered right now, and an
	// id in archive/ was already delivered — a transport retry after a lost
	// ack must be idempotent, never a re-delivery or an archive overwrite.
	base := m.ID + ".json"
	if fileExists(filepath.Join(p.Archive(), base)) {
		fmt.Printf("deposited %s\n", m.ID) // idempotent: already delivered
		return 0
	}
	if inFlight(p, base) {
		return errf(1, "spool-server: id %s is currently being delivered — refused (retry later)", m.ID)
	}
	dest := filepath.Join(p.Inbox(m.To), base)
	if _, err := os.Stat(dest); err == nil {
		return errf(1, "spool-server: id %s already present in inbox/%s — refused (no overwrite)", m.ID, m.To)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, payload) != nil {
		return errf(1, "spool-server: write failed")
	}
	if err := store.AtomicWrite(dest, append(compact.Bytes(), '\n')); err != nil {
		return errf(1, "spool-server: write failed: %v", err)
	}
	// A deposit that fits again means the backlog condition is resolved.
	warn.Clear(p, "spool-inbox-full", m.To)
	fmt.Printf("deposited %s\n", m.ID)
	// Routing advisory in the response (the registry lives on THIS side): a
	// remote sender has no other way to learn its -s target answers to nobody.
	if parsed.Session != "" && parsed.Session != peerid.Broadcast {
		ms := multi.Load(p.MultiState(m.To))
		grace := int64(envInt("WHISPER_SESSION_GRACE", 129600))
		if !ms.AnyLiveTitled(peerid.NormalizeTitle(parsed.Session), time.Now().Unix(), grace) {
			fmt.Printf("warning: no live session of %s is titled %q — message will wait, escalated after %dh\n",
				m.To, truncRunes(peerid.NormalizeTitle(parsed.Session), 64), int64(envInt("WHISPER_ROUTE_TIMEOUT", 28800))/3600)
		}
	}
	return 0
}

// confirm archives inbox/<peer>/<id>.json. Idempotent: an absent/already-archived
// id is success. The id is validated before any path op (anti-traversal).
func spoolConfirm(p store.Paths, peer, id string) int {
	if !peerid.ValidMsgID(id) {
		return errf(1, "spool-server: invalid id for confirm")
	}
	src := filepath.Join(p.Inbox(peer), id+".json")
	if _, err := os.Stat(src); err == nil {
		_ = store.EnsureDir(p.Archive())
		_ = os.Rename(src, filepath.Join(p.Archive(), id+".json"))
	}
	fmt.Printf("confirmed %s\n", id)
	return 0
}
