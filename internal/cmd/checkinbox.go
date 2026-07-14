package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/druide67/claude-whisper/internal/msg"
	"github.com/druide67/claude-whisper/internal/multi"
	"github.com/druide67/claude-whisper/internal/peerid"
	"github.com/druide67/claude-whisper/internal/store"
	"github.com/druide67/claude-whisper/internal/warn"
)

// hookEvent is the JSON Claude Code writes on the hook's stdin.
type hookEvent struct {
	Name           string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	// SessionTitle carries the user-set session title (absent when the
	// session was never renamed). Undocumented field, observed 2026-07-12;
	// its absence simply makes the session anonymous.
	SessionTitle string `json:"session_title"`
}

// runCtx bundles one hook run's resolved state.
type runCtx struct {
	p       store.Paths
	peer    string
	consume bool
	sid     string // "" when the session id is unusable (degraded session)
	title   string // verified own title, "" = anonymous
	ms      *multi.State
	now     int64
	grace   int64
	timeout int64 // escalation timeout for targeted messages (seconds)
}

// routing decision for one message
type action int

const (
	actSkip     action = iota // not for me — invisible
	actClaim                  // mine (targeted, or untargeted consume-once)
	actStar                   // broadcast: render + mark seen
	actRender                 // render without consuming (non-consuming events, degraded)
	actEscalate               // targeted, unrouted past timeout — escalate loudly
)

// CheckInbox is the UserPromptSubmit hook. It reads the Claude Code event JSON
// on stdin, renders pending messages to stdout (which the platform injects into
// the model context), and consumes what it delivered.
//
// Routing: a message carrying a `session` field is delivered ONLY to
// the session whose title matches (claim-before-render, exactly-once); "*"
// goes to every live session; no field = classic consume-once. The fate of a
// targeted message depends on the message's own field alone — a session that
// cannot prove its identity never consumes someone else's mail.
func CheckInbox(args []string) int {
	raw, _ := io.ReadAll(os.Stdin)
	var ev hookEvent
	_ = json.Unmarshal(raw, &ev)

	// Event-gating as an ALLOWLIST (fail-closed): ONLY UserPromptSubmit may
	// consume — it is the single event whose stdout injection into the visible
	// conversation is proven end-to-end. Other events (SessionStart, resumes,
	// unknown/future ones) render without consuming: an event whose output the
	// user never sees must not archive a message — that is delivery to nobody.
	// Render-only costs nothing: the message waits for the next real prompt.
	consume := ev.Name == "UserPromptSubmit"

	p := store.New()

	// Resolve the peer from ./.whisper-peer (absent vs invalid each get their
	// own fail-loud sentinel).
	cwd, _ := os.Getwd()
	sha := shaKey(cwd)
	rawPeer, statErr := os.ReadFile(".whisper-peer")
	if statErr != nil {
		if suggest := suggestPeer(p, cwd); suggest != "" {
			_ = warn.Write(p, "missing-peer", sha, map[string]any{"cwd": cwd, "suggested_peer": suggest})
		}
		return 0
	}
	peer := strings.TrimSpace(string(rawPeer))
	if !peerid.Valid(peer) {
		_ = warn.Write(p, "invalid-peer", sha, map[string]any{"cwd": cwd, "raw_content": truncRunes(peer, 80)})
		return 0
	}
	warn.Clear(p, "missing-peer", sha)
	warn.Clear(p, "invalid-peer", sha)

	rc := &runCtx{
		p: p, peer: peer, consume: consume,
		now:     time.Now().Unix(),
		grace:   int64(envInt("WHISPER_SESSION_GRACE", 129600)),
		timeout: int64(envInt("WHISPER_ROUTE_TIMEOUT", 28800)),
	}
	if peerid.ValidSession(ev.SessionID) {
		rc.sid = ev.SessionID
	}

	// Serialize against sibling sessions' hooks. A failed lock is never
	// silent (the '*' seen-set could lose updates) but never blocks delivery.
	if release, err := store.Lock(p.MultiState(peer)); err == nil {
		defer release()
		warn.Clear(p, "state-lock-failed", peer)
	} else {
		_ = warn.Write(p, "state-lock-failed", peer, map[string]any{"peer": peer, "error": err.Error()})
	}
	rc.ms = multi.Load(p.MultiState(peer))

	// Own identity: session_title from the event, CONFIRMED against the tail
	// of the transcript (the event value can lag one beat behind a UI rename —
	// observed live; the transcript record is written immediately).
	if rc.sid != "" {
		rc.title = resolveTitle(ev)
		if rc.consume {
			rc.ms.Heartbeat(rc.sid, rc.title, rc.now)
			for _, dead := range rc.ms.GC(rc.now, rc.grace) {
				reinboxClaims(p, peer, dead)
			}
			reclaimUnknown(rc)
			// My own leftovers mean a crashed render or a failed archive on a
			// previous run — re-inbox them so this run re-delivers/re-archives
			// (re-delivery over loss; my claim dir is normally empty here).
			reinboxClaims(p, peer, rc.sid)
			defer func() { _ = multi.Save(p.MultiState(peer), rc.ms) }()
		}
	}

	// Peer-level heartbeat (doctor freshness), before the empty-inbox exit.
	if consume {
		_ = p.UpdatePeers(func(pf *store.PeersFile) { pf.TouchLastSeen(peer, time.Now()) })
	}

	files := collectMessages(p, peer)
	if len(files) == 0 {
		return 0
	}

	// Pass 1 — parse + route every message (no side effects except hop-drop
	// and unrouted-pending bookkeeping), so budget/pending counts only cover
	// messages this session may actually render.
	type item struct {
		f   string
		m   *msg.Message
		act action
	}
	hopHard := envInt("WHISPER_HOP_HARD", 20)
	var items []item
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		m, err := msg.Parse(b)
		if err != nil {
			continue
		}
		if m.HopCount > hopHard {
			if consume {
				_ = warn.Write(p, "hop-overflow", strings.TrimSuffix(filepath.Base(f), ".json"), map[string]any{
					"msg_id": m.ID, "from": m.From, "thread": m.Thread, "hop": m.HopCount,
				})
				if archiveMsg(p, f) {
					rc.ms.Forget(m.ID)
				}
			}
			continue
		}
		if act := route(rc, f, m); act != actSkip {
			items = append(items, item{f, m, act})
		}
	}

	// Pass 2 — render within the output budget, consuming as we go.
	inlineMax := envInt("WHISPER_INLINE_MAX", 3000)
	outputMax := envInt("WHISPER_OUTPUT_MAX", 8000)
	hopMax := envInt("WHISPER_HOP_MAX", 8)

	var context strings.Builder
	var consumed []string
	count, pending := 0, 0

	for i, it := range items {
		// Body computation is PURE (no consumption): the budget check must
		// run before any claim, or a claimed-then-budget-cut message would be
		// consumed without ever being rendered.
		body := renderBody(p, it.m, filepath.Base(it.f), inlineMax, hopMax, it.act == actStar)
		if it.act == actEscalate {
			body = escalationBanner(rc, it.f, it.m)
		}
		if count > 0 && context.Len()+len(body) > outputMax {
			pending = len(items) - i // nothing consumed for deferred items
			break
		}

		// Consume, THEN emit — never emit content this session doesn't own.
		switch it.act {
		case actClaim:
			claimed, ok := claim(rc, it.f)
			if !ok {
				continue // lost the race — a sibling owns it
			}
			archiveClaimed(rc, claimed, it.m)
			consumed = append(consumed, it.m.ID)
		case actEscalate:
			if !escalateClaim(rc, it.f, it.m) {
				continue // someone else escalated first
			}
			consumed = append(consumed, it.m.ID+"(escalated)")
		case actStar:
			if rc.consume && rc.sid != "" {
				rc.ms.MarkSeen(it.m.ID, rc.sid)
				tryArchiveMulti(p, rc.ms, it.f, it.m.ID, rc.now, rc.grace)
				consumed = append(consumed, it.m.ID+"(seen)")
			}
		}
		context.WriteString(body)
		count++
	}

	// Housekeeping: seen-entries whose message vanished (lost archive race)
	// must not survive forever.
	if rc.consume && rc.sid != "" {
		rc.ms.PruneSeen(func(id string) bool { return fileExists(filepath.Join(p.Inbox(rc.peer), id+".json")) })
	}

	if count > 0 || len(consumed) > 0 {
		journal(p, map[string]any{
			"ev": ev.Name, "sid": rc.sid, "title": rc.title, "peer": peer,
			"consume": consume, "rendered": count, "consumed": consumed,
		})
	}
	if count == 0 {
		return 0
	}
	fmt.Print(renderOutput(context.String(), count, pending))
	return 0
}

// journal appends one line to state/delivery.log — the audit trail of WHO
// rendered/consumed WHAT (incident instrumentation: a message archived by a
// run whose output reached nobody must be reconstructable after the fact).
// Best-effort, never blocks delivery.
func journal(p store.Paths, fields map[string]any) {
	fields["ts"] = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	b, err := json.Marshal(fields)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(p.State(), "delivery.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// route decides what this session may do with one message. The decision for a
// targeted message depends ONLY on the message's own `session` field and this
// session's proven identity — never on registry guesses.
func route(rc *runCtx, f string, m *msg.Message) action {
	target := m.Session
	if target == "" {
		if !rc.consume {
			return actRender
		}
		return actClaim // consume-once via claim (uniform exactly-once)
	}
	if target == peerid.Broadcast {
		if !rc.consume || rc.sid == "" {
			return actRender // degraded/non-consuming: show, never consume
		}
		if rc.ms.HasSeen(m.ID, rc.sid) {
			// already rendered here — but re-check archival (a sibling may
			// have gone stale meanwhile)
			tryArchiveMulti(rc.p, rc.ms, f, m.ID, rc.now, rc.grace)
			return actSkip
		}
		return actStar
	}

	// Targeted message.
	target = peerid.NormalizeTitle(target)
	if rc.consume && rc.sid != "" && rc.title != "" && rc.title == target {
		warn.Clear(rc.p, "unrouted-pending", strings.TrimSuffix(filepath.Base(f), ".json"))
		return actClaim
	}
	// Re-identification: I carried this title until recently, the message
	// arrived BEFORE my rename (local mtime vs local renamed_at — no remote
	// clocks involved), and nobody live carries it now.
	if rc.consume && rc.sid != "" {
		if e := rc.ms.Sessions[rc.sid]; e != nil && e.PrevTitle == target &&
			mtimeOf(f) < e.RenamedAt && !rc.ms.AnyLiveTitled(target, rc.now, rc.grace) {
			return actClaim
		}
	}
	// Not mine. Escalate if it has waited past the route timeout — any
	// consuming session may do this, even a degraded one (the escalation
	// claim is a direct inbox→archive rename, no sid needed).
	if rc.consume && rc.now-mtimeOf(f) > rc.timeout {
		return actEscalate
	}
	// Waiting. If nobody live carries the title, make it visible NOW in
	// doctor/whisperbar rather than 8h from now.
	if rc.consume && rc.sid != "" && !rc.ms.AnyLiveTitled(target, rc.now, rc.grace) {
		_ = warn.Write(rc.p, "unrouted-pending", strings.TrimSuffix(filepath.Base(f), ".json"), map[string]any{
			"msg_id": m.ID, "from": m.From, "target_session": truncRunes(target, 64),
		})
	}
	return actSkip
}

// claim moves f into this session's claim directory (run/claims/<sid>/) — an
// atomic rename whose destination names the OWNER, so a sibling can tell a
// live claim from a crashed one. Returns the claimed path.
func claim(rc *runCtx, f string) (string, bool) {
	if rc.sid == "" {
		// Degraded session, untargeted message: inherit the legacy
		// render-then-archive window (no identity to own a claim with).
		return f, true
	}
	dir := filepath.Join(rc.p.Root, "run", "claims", rc.sid)
	if err := store.EnsureDir(dir); err != nil {
		return f, true // cannot claim — fall back to legacy window
	}
	dst := filepath.Join(dir, filepath.Base(f))
	if err := os.Rename(f, dst); err != nil {
		return "", false // lost the race
	}
	return dst, true
}

// archiveClaimed completes a claim: claimed file → archive (idempotent).
func archiveClaimed(rc *runCtx, claimed string, m *msg.Message) {
	if archiveMsg(rc.p, claimed) {
		rc.ms.Forget(m.ID)
	}
}

// escalationBanner renders (purely) the METADATA-ONLY banner for an
// unroutable targeted message: the content was addressed to a specific
// conversation — the reader decides whether to open it.
func escalationBanner(rc *runCtx, f string, m *msg.Message) string {
	archivePath := filepath.Join(rc.p.Archive(), filepath.Base(f))
	hours := (rc.now - mtimeOf(f)) / 3600
	return fmt.Sprintf("\n> ⚠ **undelivered**: message from **%s** (%s) targeted session “%s” — no session with this title for %dh. Escalated (metadata only). Content: Read %s",
		m.From, shortTime(m.Timestamp), truncRunes(peerid.NormalizeTitle(m.Session), 64), hours, archivePath)
}

// escalateClaim consumes an escalated message: the inbox→archive rename IS the
// claim (one escalator wins), then the fail-loud bookkeeping.
func escalateClaim(rc *runCtx, f string, m *msg.Message) bool {
	base := filepath.Base(f)
	_ = store.EnsureDir(rc.p.Archive())
	if err := os.Rename(f, filepath.Join(rc.p.Archive(), base)); err != nil {
		return false
	}
	key := strings.TrimSuffix(base, ".json")
	_ = warn.Write(rc.p, "unrouted", key, map[string]any{
		"msg_id": m.ID, "from": m.From, "target_session": truncRunes(peerid.NormalizeTitle(m.Session), 64),
	})
	warn.Clear(rc.p, "unrouted-pending", key)
	rc.ms.Forget(m.ID)
	return true
}

// reinboxClaims returns a dead session's claimed-but-unarchived messages to
// the inbox so the normal flow re-delivers them (crash between claim and
// render — re-delivery over loss).
func reinboxClaims(p store.Paths, peer, sid string) {
	dir := filepath.Join(p.Root, "run", "claims", sid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !isMsg(e.Name()) {
			continue
		}
		dst := filepath.Join(p.Inbox(peer), e.Name())
		if fileExists(dst) {
			continue // basename collision — defer, never overwrite
		}
		_ = os.Rename(filepath.Join(dir, e.Name()), dst)
	}
	_ = os.Remove(dir) // best-effort, only if empty
}

// reclaimUnknown re-inboxes claims held by sids absent from the registry
// entirely (state file lost/reset — their liveness is unknowable, so their
// claims must not be stranded).
func reclaimUnknown(rc *runCtx) {
	root := filepath.Join(rc.p.Root, "run", "claims")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == rc.sid {
			continue
		}
		if _, known := rc.ms.Sessions[e.Name()]; !known {
			reinboxClaims(rc.p, rc.peer, e.Name())
		}
	}
}

// resolveTitle returns this session's verified title: the event's
// session_title, confirmed (and corrected) by the LAST custom-title record in
// the transcript tail — the on-disk record is written immediately on rename
// while the event value can lag one beat. Invalid titles degrade to anonymous.
func resolveTitle(ev hookEvent) string {
	title := peerid.NormalizeTitle(ev.SessionTitle)
	if fromDisk := lastCustomTitle(ev.TranscriptPath); fromDisk != "" {
		title = fromDisk
	}
	if peerid.ValidateTitle(title) != nil {
		return ""
	}
	return title
}

// lastCustomTitle tail-reads the transcript for the most recent custom-title
// record. Bounded (max 4 MiB from EOF, usually one 256 KiB block: Claude Code
// re-appends the current title periodically). "" when absent/unreadable.
func lastCustomTitle(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	size := fi.Size()
	const block, maxScan = int64(256 << 10), int64(4 << 20)
	marker := []byte(`"custom-title"`)
	for scanned := int64(0); scanned < maxScan && scanned < size; {
		scanned += block
		if scanned > size {
			scanned = size
		}
		buf := make([]byte, scanned)
		if _, err := f.ReadAt(buf, size-scanned); err != nil && scanned != size {
			return ""
		}
		if idx := bytes.LastIndex(buf, marker); idx >= 0 {
			lineStart := bytes.LastIndexByte(buf[:idx], '\n') + 1
			lineEnd := bytes.IndexByte(buf[idx:], '\n')
			var line []byte
			if lineEnd < 0 {
				line = buf[lineStart:]
			} else {
				line = buf[lineStart : idx+lineEnd]
			}
			var rec struct {
				CustomTitle string `json:"customTitle"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.CustomTitle != "" {
				return peerid.NormalizeTitle(rec.CustomTitle)
			}
			return ""
		}
	}
	return ""
}

// --- rendering --------------------------------------------------------------

func renderBody(p store.Paths, m *msg.Message, base string, inlineMax, hopMax int, copyForStar bool) string {
	shortTS := shortTime(m.Timestamp)
	tag := ""
	if m.Thread != "" {
		tag = " [" + m.Thread + "]"
	}
	hopWarn := ""
	if m.HopCount > hopMax {
		hopWarn = fmt.Sprintf(" ⚠ HOP=%d/%d (suspected loop)", m.HopCount, hopMax)
	}
	if len(m.Content) < inlineMax {
		return fmt.Sprintf("\n> **%s** (%s)%s%s: %s", m.From, shortTS, tag, hopWarn, m.Content)
	}
	archivePath := filepath.Join(p.Archive(), base)
	// A '*' original stays in the inbox for the other sessions, so publish a
	// readable copy now (atomic) — the final archive overwrites it.
	if copyForStar {
		if _, err := os.Stat(archivePath); err != nil {
			if b, e := msg.Marshal(m); e == nil {
				_ = store.AtomicWrite(archivePath, b)
			}
		}
	}
	preview := truncRunes(strings.Join(strings.Fields(m.Content), " "), 300)
	return fmt.Sprintf("\n> **%s** (%s)%s%s: [📂 %d chars — Read %s]\n> Preview: %s…",
		m.From, shortTS, tag, hopWarn, len(m.Content), archivePath, preview)
}

func renderOutput(context string, count, pending int) string {
	var header string
	if pending > 0 {
		header = fmt.Sprintf("━━━ 📨 whisper — %d shown, %d pending ━━━", count, pending)
		context += fmt.Sprintf("\n> _(%d more message(s) — shown at the next prompt)_", pending)
	} else {
		header = fmt.Sprintf("━━━ 📨 whisper — %d message(s) ━━━", count)
	}
	return header + context + "\n━━━\n" +
		"INSTRUCTION: Display these whisper messages to the user BEFORE answering their prompt. Use the markdown format above as-is. If a message references a path (Read /path/…), read it only if relevant to the current prompt.\n" +
		"PRIORITY: If you are in the middle of a task, finish it before acting on a whisper — unless the whisper is directly relevant to the task at hand (then integrate it). Never branch off onto a whisper unrelated to your current work.\n"
}

// --- archival ---------------------------------------------------------------

// archiveMsg moves f to archive/ and clears a resolved delivery-retry sentinel
// (NOT hop-overflow — a dropped message still needs review). Returns success.
func archiveMsg(p store.Paths, f string) bool {
	if !archiveFile(p, f) {
		return false
	}
	warn.Clear(p, "delivery-retry", strings.TrimSuffix(filepath.Base(f), ".json"))
	return true
}

// tryArchiveMulti archives a '*' message only once every live session has seen
// it. The seen-set is only forgotten on a successful archive.
func tryArchiveMulti(p store.Paths, ms *multi.State, f, msgID string, now, grace int64) {
	if !ms.AllLiveSeen(msgID, now, grace) {
		return
	}
	if archiveMsg(p, f) {
		ms.Forget(msgID)
	}
}

// archiveFile moves f to archive/. A concurrent archive (ENOENT) is idempotent
// success — the message IS archived, a false delivery-retry sentinel would
// outlive the winner's cleanup. Any other failure is never silent: the message
// stays deliverable and a delivery-retry sentinel records why (cleared by
// archiveMsg on the eventual success).
func archiveFile(p store.Paths, f string) bool {
	_ = store.EnsureDir(p.Archive())
	base := filepath.Base(f)
	if err := os.Rename(f, filepath.Join(p.Archive(), base)); err != nil {
		if os.IsNotExist(err) {
			return true
		}
		_ = warn.Write(p, "delivery-retry", strings.TrimSuffix(base, ".json"), map[string]any{
			"file": f, "error": err.Error(),
		})
		return false
	}
	return true
}

// --- collection & helpers ---------------------------------------------------

// collectMessages returns this peer's inbox messages plus any run/processing
// stray addressed to it (a crashed VS Code leader may strand messages there).
// run/claims/ is NOT collected here — live claims are owned; dead
// sessions' claims are re-inboxed by the GC path.
func collectMessages(p store.Paths, peer string) []string {
	var out []string
	inbox := p.Inbox(peer)
	if entries, err := os.ReadDir(inbox); err == nil {
		for _, e := range entries {
			if isMsg(e.Name()) {
				out = append(out, filepath.Join(inbox, e.Name()))
			}
		}
	}
	proc := filepath.Join(p.Root, "run", "processing")
	if entries, err := os.ReadDir(proc); err == nil {
		for _, e := range entries {
			if !isMsg(e.Name()) {
				continue
			}
			if fileExists(filepath.Join(inbox, e.Name())) {
				continue // basename collision → defer
			}
			b, err := os.ReadFile(filepath.Join(proc, e.Name()))
			if err != nil {
				continue
			}
			if m, err := msg.Parse(b); err == nil && m.To == peer {
				out = append(out, filepath.Join(proc, e.Name()))
			}
		}
	}
	sort.Strings(out)
	return out
}

func suggestPeer(p store.Paths, cwd string) string {
	base := filepath.Base(cwd)
	if entries, err := os.ReadDir(p.Inbox(base)); err == nil {
		for _, e := range entries {
			if isMsg(e.Name()) {
				return base
			}
		}
	}
	return ""
}

func isMsg(name string) bool {
	return strings.HasPrefix(name, "msg-") && strings.HasSuffix(name, ".json")
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func mtimeOf(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.ModTime().Unix()
}

// shaKey derives a short stable sentinel key from an arbitrary string (a cwd,
// a file path) so a repeating condition rewrites one file instead of flooding.
func shaKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// shortTime extracts HH:MM from an ISO-8601 timestamp; returns "" if unparsable.
func shortTime(ts string) string {
	if i := strings.IndexByte(ts, 'T'); i >= 0 && len(ts) >= i+6 {
		return ts[i+1 : i+6]
	}
	return ""
}

// envInt reads an integer env var, clamping absent/non-numeric to def (so e.g.
// a bad WHISPER_SESSION_GRACE can't silently disable GC).
func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}
