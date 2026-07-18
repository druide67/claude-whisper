package cmd

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/druide67/claude-whisper/internal/msg"
	"github.com/druide67/claude-whisper/internal/multi"
	"github.com/druide67/claude-whisper/internal/peerid"
	"github.com/druide67/claude-whisper/internal/store"
	"github.com/druide67/claude-whisper/internal/warn"
)

type sendOpts struct {
	thread, from, replyTo, session, priority string
	force                                    bool
}

// Send implements:
//
//	whisper send [-t thread] [-f from] [-r reply-to] [-s session-title|'*'] [-p normal|urgent] [-F] <peer> <message>
func Send(args []string) int {
	var o sendOpts
	rest, err := parseSendFlags(args, &o)
	if err != nil {
		return errf(1, "%v", err)
	}
	if len(rest) < 2 {
		return errf(1, "Usage: whisper send [-t thread] [-f from] [-r reply-to] [-s session|'*'] [-p normal|urgent] [-F] <peer> <message>")
	}
	to := rest[0]
	content := strings.Join(rest[1:], " ")

	if !peerid.Valid(to) {
		return errf(1, "Error: peer-id must be alphanumeric + dash (got: %s)", to)
	}
	if content == "" {
		return errf(1, "Error: message cannot be empty.")
	}

	from := o.from
	if from == "" {
		from = readSelfPeer()
	}
	if from == "" {
		return errf(1, "Error: not initialized. Run whisper init or use --from.")
	}
	if !peerid.Valid(from) {
		return errf(1, "Error: corrupted .whisper-peer (got: %s)", from)
	}

	p := store.New()
	return sendMessage(p, to, from, content, o)
}

func sendMessage(p store.Paths, to, from, content string, o sendOpts) int {
	// A malformed --reply-to aborts (bash parity): sending anyway would break
	// the hop chain that loop detection relies on. Checked before the dedup
	// ledger so an aborted send doesn't block its own retry.
	if o.replyTo != "" && !peerid.ValidMsgID(o.replyTo) {
		return errf(1, "Error: --reply-to expects a msg-<ts>-<hex> id (got: %s)", o.replyTo)
	}
	// Session target: normalize + validate the address grammar, then advise
	// (never block) when nothing live currently answers to it.
	if o.session != "" && o.session != peerid.Broadcast {
		o.session = peerid.NormalizeTitle(o.session)
		if err := peerid.ValidateTitle(o.session); err != nil {
			return errf(1, "Error: --session: %v", err)
		}
		adviseSessionTarget(p, to, o.session)
	}

	// Fail-loud on an unregistered recipient (unless --force): an orphan inbox
	// nobody reads = a silently lost message.
	if _, statErr := os.Stat(p.PeersFile()); statErr == nil {
		pf, err := p.ReadPeers()
		if err == nil && o.session != "" {
			if e, ok := pf.Peers[to]; ok {
				if _, isTransport := e["transport"]; isTransport {
					fmt.Fprintf(os.Stderr, "⚠️  whisper send: %q is a transport peer — its remote binary may predate session routing and ignore -s (mis-delivery risk). Deploy servers first.\n", to)
				}
			}
		}
		if err == nil && !pf.IsRegistered(to) {
			if o.force {
				fmt.Fprintf(os.Stderr, "⚠️  whisper send: peer %q not in peers.json — sending anyway (--force).\n", to)
			} else {
				known := strings.Join(pf.SortedIDs(), ", ")
				fmt.Fprintf(os.Stderr, "Error: peer %q is not a registered peer (peers.json).\n", to)
				fmt.Fprintf(os.Stderr, "       Known peers: %s\n", known)
				fmt.Fprintf(os.Stderr, "       Check spelling, run whisper list, or use --force for a new peer.\n")
				return 1
			}
		}
	}

	now := time.Now()
	if !dedupOK(p, from, to, content, now) {
		return 0 // duplicate within window: refused, not an error
	}

	// Anti-loop circuit breaker: rate-limit the unordered {from,to} pair. A
	// ping-pong loop between two agents shows up here even when no explicit
	// reply chain (hop_count) links the messages.
	if !pairBreakerOK(p, from, to, now) {
		return 1
	}

	hop := resolveHop(p, o.replyTo, to, from)

	prio := o.priority
	if prio == "" {
		prio = "normal"
	}
	rnd := make([]byte, 4)
	_, _ = rand.Read(rnd)
	id := msg.NewID(now.Unix(), rnd)
	m := &msg.Message{
		ID: id, From: from, To: to,
		Timestamp: msg.FormatTimestamp(now), Content: content,
		Priority: prio, TTL: 3600, HopCount: hop,
		Thread: o.thread, InReplyTo: o.replyTo, Session: o.session,
	}
	b, err := msg.Marshal(m)
	if err != nil {
		return errf(1, "Error: marshal: %v", err)
	}
	if err := store.AtomicWrite(filepath.Join(p.Inbox(to), id+".json"), b); err != nil {
		return errf(1, "Error: write message: %v", err)
	}

	info := ""
	if o.thread != "" {
		info = fmt.Sprintf(" [%s hop=%d]", o.thread, hop)
	}
	fmt.Printf("📤 whisper → %s%s: sent (%s)\n", to, info, id)
	return 0
}

func parseSendFlags(args []string, o *sendOpts) ([]string, error) {
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-t", "--thread":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--thread needs a value")
			}
			o.thread = args[i+1]
			i += 2
		case "-f", "--from":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--from needs a value")
			}
			o.from = args[i+1]
			i += 2
		case "-r", "--reply-to":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--reply-to needs a value")
			}
			o.replyTo = args[i+1]
			i += 2
		case "-s", "--session":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--session needs a value (a session title, or '*')")
			}
			o.session = args[i+1]
			i += 2
		case "-p", "--priority":
			if i+1 >= len(args) {
				return nil, fmt.Errorf(`--priority needs a value ("normal" or "urgent")`)
			}
			if v := args[i+1]; v != "normal" && v != "urgent" {
				return nil, fmt.Errorf(`--priority must be "normal" or "urgent" (got: %s)`, v)
			}
			o.priority = args[i+1]
			i += 2
		case "-F", "--force":
			o.force = true
			i++
		default:
			return args[i:], nil
		}
	}
	return args[i:], nil
}

// --- dedup ledger (state/recent-sends.json) --------------------------------

type ledger struct {
	Entries []ledgerEntry `json:"entries"`
}
type ledgerEntry struct {
	TS  int64  `json:"ts"`
	Key string `json:"key"`
}

func dupWindow() int64 {
	if v := os.Getenv("WHISPER_DUP_WINDOW"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 30
}

// dedupOK returns false if this (from,to,content) was sent within the window.
// It also appends this send to the ledger and prunes stale entries. A corrupt
// ledger is surfaced as a fail-loud sentinel and reset (this send isn't deduped
// but the operator is told).
func dedupOK(p store.Paths, from, to, content string, now time.Time) bool {
	// Hold the ledger lock across read-modify-write: without it, concurrent
	// sends lost-update the ledger and dedup silently breaks (audit: 60
	// concurrent sends → 1 entry survived).
	if release, err := store.Lock(p.RecentSends()); err == nil {
		defer release()
	}
	window := dupWindow()
	sum := sha256.Sum256([]byte(content))
	key := from + "|" + to + "|" + hex.EncodeToString(sum[:])[:16]
	nowTS := now.Unix()

	var l ledger
	valid := true
	if b, err := os.ReadFile(p.RecentSends()); err == nil {
		if json.Unmarshal(b, &l) != nil || l.Entries == nil {
			valid = false
		}
	}
	if !valid {
		_ = warn.Write(p, "recent-sends-corrupt", "", map[string]any{
			"detail": "reset; G2 dedup skipped for this send",
		})
		fmt.Fprintln(os.Stderr, "⚠️  whisper send: recent-sends.json corrupt — reset (anti-dup ledger rebuilt). See whisper doctor.")
		_ = os.Remove(p.RecentSends())
		l = ledger{}
	} else {
		for _, e := range l.Entries {
			if e.Key == key && e.TS > nowTS-window {
				fmt.Fprintf(os.Stderr, "⚠️  whisper send: duplicate within %ds — refused.\n", window)
				return false
			}
		}
	}

	// append + prune
	kept := l.Entries[:0]
	for _, e := range l.Entries {
		if e.TS > nowTS-window {
			kept = append(kept, e)
		}
	}
	kept = append(kept, ledgerEntry{TS: nowTS, Key: key})
	_ = store.AtomicWriteJSON(p.RecentSends(), ledger{Entries: kept})
	return true
}

// adviseSessionTarget warns (stderr, never blocks) when no live session of the
// recipient currently carries the targeted title — a typo means 8h of silence
// before escalation, so surface it at send time. The registry is advisory only.
func adviseSessionTarget(p store.Paths, to, title string) {
	ms := multi.Load(p.MultiState(to))
	now := time.Now().Unix()
	grace := int64(envInt("WHISPER_SESSION_GRACE", 129600))
	if ms.AnyLiveTitled(title, now, grace) {
		return
	}
	titles, anonymous := ms.LiveTitles(now, grace)
	fmt.Fprintf(os.Stderr, "⚠️  whisper send: no live session of %q is titled %q — the message will wait (escalated after %dh).\n",
		to, title, int64(envInt("WHISPER_ROUTE_TIMEOUT", 28800))/3600)
	if len(titles) > 0 {
		fmt.Fprintf(os.Stderr, "    Live titled sessions: %s\n", strings.Join(titles, ", "))
	}
	if anonymous > 0 {
		fmt.Fprintf(os.Stderr, "    Plus %d anonymous session(s) — a session must be renamed (UI pencil) to be targetable.\n", anonymous)
	}
}

// resolveHop follows the explicit reply chain: find <replyTo>.json across the
// likely dirs, read its hop_count, add 1. Missing ref → hop 0 with a warning.
// The id's shape is validated (abort) in sendMessage before we get here.
func resolveHop(p store.Paths, replyTo, to, from string) int {
	if replyTo == "" {
		return 0
	}
	for _, dir := range []string{p.Archive(), p.Inbox(to), p.Inbox(from), filepath.Join(p.Root, "run", "processing")} {
		cand := filepath.Join(dir, replyTo+".json")
		if b, err := os.ReadFile(cand); err == nil {
			if m, err := msg.Parse(b); err == nil {
				return m.HopCount + 1
			}
		}
	}
	fmt.Fprintf(os.Stderr, "⚠️  whisper send: --reply-to %s not found — sending as hop=0\n", replyTo)
	return 0
}
