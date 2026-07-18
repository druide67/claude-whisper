package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/druide67/claude-whisper/internal/msg"
	"github.com/druide67/claude-whisper/internal/store"
)

// --- harness ---------------------------------------------------------------

// sandbox points WHISPER_DIR at a temp dir and returns a store.Paths for it.
func sandbox(t *testing.T) store.Paths {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WHISPER_DIR", dir)
	return store.Paths{Root: dir}
}

// capture swaps os.Stdin/os.Stdout for the duration of fn, feeding stdin and
// returning captured stdout + the exit code.
func capture(t *testing.T, stdin string, fn func() int) (string, int) {
	t.Helper()
	oldIn, oldOut := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	inR, inW, _ := os.Pipe()
	go func() { io.WriteString(inW, stdin); inW.Close() }()
	os.Stdin = inR

	outR, outW, _ := os.Pipe()
	os.Stdout = outW
	done := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); done <- string(b) }()

	code := fn()
	outW.Close()
	out := <-done
	inR.Close()
	return out, code
}

// initPeer registers a local peer whose project dir is a fresh temp dir.
func initPeer(t *testing.T, id string) string {
	t.Helper()
	proj := t.TempDir()
	_, code := capture(t, "", func() int { return Init([]string{id, proj}) }) // silence init output
	if code != 0 {
		t.Fatalf("init %s failed (code %d)", id, code)
	}
	return proj
}

func countInbox(p store.Paths, peer string) int { return pendingCount(p, peer) }

// --- send -------------------------------------------------------------------

func TestSendSchemaAndDedup(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	initPeer(t, "bob")

	if _, code := capture(t, "", func() int { return Send([]string{"-f", "alice", "-t", "demo", "bob", "hello bob"}) }); code != 0 {
		t.Fatalf("send exit %d", code)
	}
	entries, _ := os.ReadDir(p.Inbox("bob"))
	var msgFile string
	for _, e := range entries {
		if isMsg(e.Name()) {
			msgFile = filepath.Join(p.Inbox("bob"), e.Name())
		}
	}
	if msgFile == "" {
		t.Fatal("no message written")
	}
	b, _ := os.ReadFile(msgFile)
	s := string(b)
	for _, want := range []string{`"from":"alice"`, `"to":"bob"`, `"priority":"normal"`, `"ttl":3600`, `"hop_count":0`, `"thread":"demo"`} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %s: %s", want, s)
		}
	}
	// dedup key is from|to|hash(content), thread-independent: an identical
	// resend within the window is refused; a different content is delivered.
	capture(t, "", func() int { return Send([]string{"-f", "alice", "bob", "hello bob"}) }) // dup of msg 1 → refused
	if n := countInbox(p, "bob"); n != 1 {
		t.Errorf("dedup: identical resend should be refused, got %d messages want 1", n)
	}
	capture(t, "", func() int { return Send([]string{"-f", "alice", "bob", "different"}) }) // new content → delivered
	if n := countInbox(p, "bob"); n != 2 {
		t.Errorf("distinct content should be delivered, got %d want 2", n)
	}
}

func TestSendUnknownRecipientFailLoud(t *testing.T) {
	sandbox(t)
	initPeer(t, "alice")
	_, code := capture(t, "", func() int { return Send([]string{"-f", "alice", "ghost", "x"}) })
	if code != 1 {
		t.Errorf("send to unknown peer should exit 1, got %d", code)
	}
	// --force overrides
	if _, code := capture(t, "", func() int { return Send([]string{"-f", "alice", "-F", "ghost", "x"}) }); code != 0 {
		t.Errorf("send --force to new peer should exit 0, got %d", code)
	}
}

func TestSendReplyToMalformedAborts(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	initPeer(t, "bob")
	_, code := capture(t, "", func() int {
		return Send([]string{"-f", "alice", "-r", "not-a-msg-id", "bob", "x"})
	})
	if code != 1 {
		t.Errorf("malformed --reply-to must abort (exit 1, bash parity), got %d", code)
	}
	if countInbox(p, "bob") != 0 {
		t.Error("aborted send must not write a message")
	}
	// the abort must not have burned the dedup window: an immediate valid retry
	// of the same content goes through
	if _, code := capture(t, "", func() int { return Send([]string{"-f", "alice", "bob", "x"}) }); code != 0 {
		t.Errorf("retry after aborted send should succeed, got exit %d", code)
	}
}

// --- check-inbox ------------------------------------------------------------

func TestCheckInboxDeliverAndArchive(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	capture(t, "", func() int { return Send([]string{"-f", "alice", "bob", "coucou"}) })

	out, code := capture(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess1234"}`, func() int { return CheckInbox(nil) })
	if code != 0 || !strings.Contains(out, "coucou") || !strings.Contains(out, "whisper") {
		t.Fatalf("delivery failed: code=%d out=%q", code, out)
	}
	if countInbox(p, "bob") != 0 {
		t.Error("message should be archived after a consuming run")
	}
}

func TestCheckInboxEventGatingNoConsume(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	capture(t, "", func() int { return Send([]string{"-f", "alice", "bob", "notif-test"}) })

	// Notification is NOT an injecting event → rendered but NOT consumed.
	out, _ := capture(t, `{"hook_event_name":"Notification","session_id":"sess1234"}`, func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "notif-test") {
		t.Error("Notification should still render")
	}
	if countInbox(p, "bob") != 1 {
		t.Error("Notification must NOT consume the message (fail-closed gating)")
	}
}

// ev builds a hook event with optional session title.
func ev(sid, title string) string {
	e := map[string]string{"hook_event_name": "UserPromptSubmit", "session_id": sid}
	if title != "" {
		e["session_title"] = title
	}
	b, _ := json.Marshal(e)
	return string(b)
}

func TestCheckInboxStarHoldUntilAllSeen(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	// register two live sessions first (empty inbox)
	capture(t, ev("sessAAAA1", ""), func() int { return CheckInbox(nil) })
	capture(t, ev("sessBBBB2", ""), func() int { return CheckInbox(nil) })
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "*", "bob", "holdstar"}) })

	out, _ := capture(t, ev("sessAAAA1", ""), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "holdstar") {
		t.Fatal("'*' must render to session A")
	}
	if countInbox(p, "bob") != 1 {
		t.Fatal("'*' must be HELD until every live session has seen it")
	}
	out, _ = capture(t, ev("sessBBBB2", ""), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "holdstar") {
		t.Fatal("'*' must render to session B too")
	}
	if countInbox(p, "bob") != 0 {
		t.Fatal("'*' must be archived once all live sessions saw it")
	}
	// A re-run of A must not re-inject (no duplicate)
	out, _ = capture(t, ev("sessAAAA1", ""), func() int { return CheckInbox(nil) })
	if strings.Contains(out, "holdstar") {
		t.Error("archived '*' re-injected")
	}
}

// --- targeted session routing -------------------------------------------------

func TestTargetedOnlyMatchingSessionDelivers(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "SEO", "bob", "pour-seo"}) })

	// wrong title → invisible, not consumed
	out, _ := capture(t, ev("sessDEV12", "DEV"), func() int { return CheckInbox(nil) })
	if strings.Contains(out, "pour-seo") {
		t.Fatal("targeted message rendered to the WRONG session")
	}
	// anonymous → invisible
	out, _ = capture(t, ev("sessANON1", ""), func() int { return CheckInbox(nil) })
	if strings.Contains(out, "pour-seo") {
		t.Fatal("targeted message rendered to an anonymous session")
	}
	if countInbox(p, "bob") != 1 {
		t.Fatal("message must still be waiting")
	}
	// matching title → delivered + archived (exactly-once)
	out, _ = capture(t, ev("sessSEO12", "SEO"), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "pour-seo") {
		t.Fatal("targeted message NOT delivered to the matching session")
	}
	if countInbox(p, "bob") != 0 {
		t.Error("delivered targeted message must be archived")
	}
	// homonym arriving later: nothing left
	out, _ = capture(t, ev("sessSEO34", "SEO"), func() int { return CheckInbox(nil) })
	if strings.Contains(out, "pour-seo") {
		t.Error("second homonym must not re-render (exactly-once)")
	}
}

func TestTargetedDegradedSessionNeverConsumes(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "SEO", "bob", "secret-seo"}) })
	// degraded: invalid session id — even with a matching title in the event,
	// an identity that can't be proven must never consume someone's mail
	out, _ := capture(t, `{"hook_event_name":"UserPromptSubmit","session_id":"!!","session_title":"SEO"}`,
		func() int { return CheckInbox(nil) })
	if strings.Contains(out, "secret-seo") {
		t.Fatal("degraded session consumed a targeted message")
	}
	if countInbox(p, "bob") != 1 {
		t.Fatal("message must still be in inbox")
	}
}

func TestTargetedEscalationAfterTimeout(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "Ghost", "bob", "contenu-prive"}) })
	t.Setenv("WHISPER_ROUTE_TIMEOUT", "0")
	// age the message past the (zero) timeout
	entries, _ := os.ReadDir(p.Inbox("bob"))
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(filepath.Join(p.Inbox("bob"), entries[0].Name()), old, old)

	out, _ := capture(t, ev("sessANY12", "Autre"), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "undelivered") || !strings.Contains(out, "Ghost") {
		t.Fatalf("escalation banner missing: %q", out)
	}
	if strings.Contains(out, "contenu-prive") {
		t.Error("escalation must be METADATA ONLY, never the content")
	}
	if countInbox(p, "bob") != 0 {
		t.Error("escalated message must be archived")
	}
	// The banner was rendered AND journaled by this very run: the unrouted
	// condition is resolved, so the sentinel written at escalation must be
	// cleared before the run ends (it only survives a crash mid-escalation).
	warns, _ := filepath.Glob(filepath.Join(p.Warnings(), "unrouted-*.warn"))
	if len(warns) != 0 {
		t.Errorf("rendered escalation must clear its unrouted sentinel, found %v", warns)
	}
	b, err := os.ReadFile(filepath.Join(p.State(), "delivery.log"))
	if err != nil || !strings.Contains(string(b), "(escalated)") {
		t.Errorf("escalation must be journaled: %v %s", err, b)
	}
}

func TestStarNeverEscalates(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	// one live session marks it seen; a second (silent) session holds it
	capture(t, ev("sessAAAA1", ""), func() int { return CheckInbox(nil) })
	capture(t, ev("sessBBBB2", ""), func() int { return CheckInbox(nil) })
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "*", "bob", "fyi"}) })
	t.Setenv("WHISPER_ROUTE_TIMEOUT", "0")
	capture(t, ev("sessAAAA1", ""), func() int { return CheckInbox(nil) })
	if countInbox(p, "bob") != 1 {
		t.Fatal("'*' must never be escalated/archived before the grace expires")
	}
}

func TestReidentificationAfterRename(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	// session lives as "Old"
	capture(t, ev("sessRENAM1", "Old"), func() int { return CheckInbox(nil) })
	// a message targets "Old", arriving BEFORE the rename
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "Old", "bob", "pre-rename"}) })
	entries, _ := os.ReadDir(p.Inbox("bob"))
	f := filepath.Join(p.Inbox("bob"), entries[0].Name())
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(f, past, past)
	// the session prompts under its NEW title → rename observed → re-identify
	out, _ := capture(t, ev("sessRENAM1", "New"), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "pre-rename") {
		t.Fatal("pre-rename message must be re-identified and delivered")
	}
	if countInbox(p, "bob") != 0 {
		t.Error("re-identified message must be archived")
	}

	// a message targeting "Old" AFTER the rename must NOT be re-identified
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "Old", "bob", "post-rename"}) })
	entries, _ = os.ReadDir(p.Inbox("bob"))
	f = filepath.Join(p.Inbox("bob"), entries[0].Name())
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(f, future, future)
	out, _ = capture(t, ev("sessRENAM1", "New"), func() int { return CheckInbox(nil) })
	if strings.Contains(out, "post-rename") {
		t.Fatal("post-rename message must NOT be stolen via re-identification")
	}
}

func TestVerifyBeforeClaimTranscriptWins(t *testing.T) { // E1
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	// transcript says "Right" (last custom-title record); event lags on "Wrong"
	transcript := filepath.Join(t.TempDir(), "sess.jsonl")
	_ = os.WriteFile(transcript, []byte(
		`{"type":"custom-title","customTitle":"Wrong","sessionId":"x"}`+"\n"+
			`{"type":"custom-title","customTitle":"Right","sessionId":"x"}`+"\n"), 0o600)
	evJSON := `{"hook_event_name":"UserPromptSubmit","session_id":"sessE1XX1","session_title":"Wrong","transcript_path":"` + transcript + `"}`

	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "Right", "bob", "pour-right"}) })
	out, _ := capture(t, evJSON, func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "pour-right") {
		t.Fatal("the transcript title must override the lagging event title")
	}
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "Wrong", "bob", "pour-wrong"}) })
	out, _ = capture(t, evJSON, func() int { return CheckInbox(nil) })
	if strings.Contains(out, "pour-wrong") {
		t.Fatal("the stale event title must not be used to claim")
	}
	if countInbox(p, "bob") != 1 {
		t.Error("mis-targeted message must wait")
	}
}

func TestSendSessionGrammar(t *testing.T) {
	sandbox(t)
	initPeer(t, "alice")
	initPeer(t, "bob")
	if _, code := capture(t, "", func() int {
		return Send([]string{"-f", "alice", "-s", "bad\ntitle", "bob", "x"})
	}); code != 1 {
		t.Error("control chars in -s must abort")
	}
	long := strings.Repeat("a", 65)
	if _, code := capture(t, "", func() int {
		return Send([]string{"-f", "alice", "-s", long, "bob", "x"})
	}); code != 1 {
		t.Error("65-rune title must abort")
	}
	if _, code := capture(t, "", func() int {
		return Send([]string{"-f", "alice", "-s", "*", "bob", "broadcast-ok"})
	}); code != 0 {
		t.Error("-s '*' must be accepted")
	}
}

func TestCheckInboxArchiveFailureFailsLoud(t *testing.T) { // archive failure + claim recovery
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	capture(t, "", func() int { return Send([]string{"-f", "alice", "bob", "stuck"}) })

	// Sabotage: a regular file where archive/ should be → rename must fail.
	if err := os.WriteFile(p.Archive(), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := capture(t, ev("sess1234", ""), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "stuck") {
		t.Fatal("message should still render")
	}
	warns, _ := filepath.Glob(filepath.Join(p.Warnings(), "delivery-retry-*.warn"))
	if len(warns) != 1 {
		t.Fatalf("archive failure must write a delivery-retry sentinel, found %d", len(warns))
	}
	// Still deliverable: the failed claim is re-inboxed and re-rendered next run.
	out, _ = capture(t, ev("sess1234", ""), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "stuck") {
		t.Fatal("message must stay deliverable while archiving is broken")
	}

	// Repair: archive works again → next run delivers and clears the sentinel.
	os.Remove(p.Archive())
	capture(t, ev("sess1234", ""), func() int { return CheckInbox(nil) })
	if countInbox(p, "bob") != 0 {
		t.Error("message should archive once the failure is repaired")
	}
	if n := len(globClaims(p)); n != 0 {
		t.Errorf("no claim should remain, found %d", n)
	}
	warns, _ = filepath.Glob(filepath.Join(p.Warnings(), "delivery-retry-*.warn"))
	if len(warns) != 0 {
		t.Error("resolved delivery-retry sentinel must be auto-cleared")
	}
}

func globClaims(p store.Paths) []string {
	out, _ := filepath.Glob(filepath.Join(p.Root, "run", "claims", "*", "msg-*.json"))
	return out
}

// --- spool-server -----------------------------------------------------------

func spoolDep(t *testing.T, peer, payload string) (string, int) {
	t.Helper()
	t.Setenv("SSH_ORIGINAL_COMMAND", "deposit")
	return capture(t, payload, func() int { return SpoolServer([]string{peer}) })
}

func TestSpoolDepositFromForce(t *testing.T) {
	sandbox(t)
	initPeer(t, "bob")
	initPeer(t, "remote-agent")
	// from != peer → identity spoof rejected
	_, code := spoolDep(t, "remote-agent", `{"id":"msg-1-aa","from":"bob","to":"bob","content":"x"}`)
	if code != 1 {
		t.Errorf("from-force: spoof should be rejected (exit 1), got %d", code)
	}
}

func TestSpoolDepositMalformedRejected(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "bob")
	initPeer(t, "remote-agent")
	// ttl wrongly typed → check-inbox could never parse it → deposit must refuse
	_, code := spoolDep(t, "remote-agent", `{"id":"msg-1-aa","from":"remote-agent","to":"bob","content":"x","ttl":"not-an-int"}`)
	if code != 1 {
		t.Errorf("malformed-typed message should be refused, got exit %d", code)
	}
	if countInbox(p, "bob") != 0 {
		t.Error("malformed message must NOT be written to inbox")
	}
}

func TestSpoolDepositTraversalRejected(t *testing.T) {
	sandbox(t)
	initPeer(t, "remote-agent")
	for _, to := range []string{"../../../etc", "../evil", "a/b"} {
		_, code := spoolDep(t, "remote-agent", `{"id":"msg-1-aa","from":"remote-agent","to":"`+to+`","content":"x"}`)
		if code != 1 {
			t.Errorf("traversal to=%q should be rejected, got %d", to, code)
		}
	}
}

func TestSpoolDepositBacklogCap(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "bob")
	initPeer(t, "remote-agent")
	t.Setenv("WHISPER_SPOOL_MAX_PENDING", "2")
	dep := func(i int) (string, int) {
		return spoolDep(t, "remote-agent",
			`{"id":"msg-`+string(rune('1'+i))+`-aa","from":"remote-agent","to":"bob","content":"x"}`)
	}
	for i := 0; i < 2; i++ {
		if _, code := dep(i); code != 0 {
			t.Fatalf("deposit %d under cap should succeed, got %d", i, code)
		}
	}
	if _, code := dep(2); code != 1 {
		t.Errorf("deposit over cap must be refused (exit 1), got %d", code)
	}
	if countInbox(p, "bob") != 2 {
		t.Errorf("inbox must stay at cap, got %d", countInbox(p, "bob"))
	}
	warns, _ := filepath.Glob(filepath.Join(p.Warnings(), "spool-inbox-full-*.warn"))
	if len(warns) != 1 {
		t.Fatalf("over-cap deposit must write a spool-inbox-full sentinel, found %d", len(warns))
	}
	// Drain one, deposit fits again → accepted and sentinel auto-cleared.
	entries, _ := os.ReadDir(p.Inbox("bob"))
	os.Remove(filepath.Join(p.Inbox("bob"), entries[0].Name()))
	if _, code := dep(3); code != 0 {
		t.Errorf("deposit after drain should succeed, got %d", code)
	}
	warns, _ = filepath.Glob(filepath.Join(p.Warnings(), "spool-inbox-full-*.warn"))
	if len(warns) != 0 {
		t.Error("resolved spool-inbox-full sentinel must be auto-cleared")
	}
}

func TestSpoolUnknownVerbNotExecuted(t *testing.T) {
	sandbox(t)
	initPeer(t, "remote-agent")
	t.Setenv("SSH_ORIGINAL_COMMAND", "rm -rf /")
	_, code := capture(t, "", func() int { return SpoolServer([]string{"remote-agent"}) })
	if code != 1 {
		t.Errorf("unknown verb should be rejected (exit 1), got %d", code)
	}
}

// --- init settings.json guard ---------------------------------------------

func TestInstallHookDoesNotClobberMalformedSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// installHook only runs when WHISPER_DIR == ~/.claude-whisper
	t.Setenv("WHISPER_DIR", filepath.Join(home, ".claude-whisper"))

	settingsDir := filepath.Join(home, ".claude")
	os.MkdirAll(settingsDir, 0o755)
	settings := filepath.Join(settingsDir, "settings.json")
	malformed := `{"model":"opus",}` // trailing comma — invalid JSON
	os.WriteFile(settings, []byte(malformed), 0o644)

	status := installHook(store.New())
	if !strings.Contains(status, "unparseable") {
		t.Errorf("expected installHook to skip on unparseable settings, got %q", status)
	}
	got, _ := os.ReadFile(settings)
	if string(got) != malformed {
		t.Errorf("settings.json was clobbered: %q", got)
	}
}

func TestTruncRunesUTF8Safe(t *testing.T) {
	s := "héllo wörld ✓✓✓"
	got := truncRunes(s, 7)
	if got != "héllo w" {
		t.Errorf("truncRunes = %q, want %q", got, "héllo w")
	}
	if truncRunes("short", 300) != "short" {
		t.Error("no-op expected under the limit")
	}
	for _, r := range truncRunes(s, 13) {
		if r == '�' {
			t.Fatal("truncation produced an invalid rune")
		}
	}
}

func TestSpoolDepositSessionValidation(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "bob")
	initPeer(t, "remote-agent")
	// hostile title from the network → refused
	_, code := spoolDep(t, "remote-agent",
		`{"id":"msg-1-aa","from":"remote-agent","to":"bob","content":"x","session":"evil\ntitle"}`)
	if code != 1 {
		t.Error("control chars in session target must be refused at deposit")
	}
	if countInbox(p, "bob") != 0 {
		t.Error("refused deposit must not write")
	}
	// unknown-but-valid title → accepted with a routing advisory in the reply
	out, code := spoolDep(t, "remote-agent",
		`{"id":"msg-2-bb","from":"remote-agent","to":"bob","content":"x","session":"NoSuchSession"}`)
	if code != 0 || !strings.Contains(out, "deposited") {
		t.Fatalf("valid targeted deposit must succeed: %q", out)
	}
	if !strings.Contains(out, "warning") || !strings.Contains(out, "NoSuchSession") {
		t.Errorf("deposit reply must carry the routing advisory, got %q", out)
	}
}

func TestSpoolDepositIdempotentOnArchivedId(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	initPeer(t, "remote-agent")
	t.Chdir(proj)
	payload := `{"id":"msg-3-cc","from":"remote-agent","to":"bob","content":"once"}`
	if _, code := spoolDep(t, "remote-agent", payload); code != 0 {
		t.Fatal("first deposit must succeed")
	}
	capture(t, ev("sess1234", ""), func() int { return CheckInbox(nil) }) // delivered + archived
	if countInbox(p, "bob") != 0 {
		t.Fatal("precondition: message must be archived")
	}
	// transport retry after a lost ack: same id again → idempotent success,
	// nothing re-enters the inbox, the archive is not overwritten
	out, code := spoolDep(t, "remote-agent", payload)
	if code != 0 || !strings.Contains(out, "deposited") {
		t.Errorf("retry on archived id must be idempotent success, got code=%d %q", code, out)
	}
	if countInbox(p, "bob") != 0 {
		t.Error("retry must NOT re-deliver")
	}
}

func TestOnlyUserPromptSubmitConsumes(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	capture(t, "", func() int { return Send([]string{"-f", "alice", "bob", "precieux"}) })
	// SessionStart and the empty event render but NEVER consume: their stdout
	// injection is unproven — 3 real messages were archived toward nobody.
	for _, evJSON := range []string{
		`{"hook_event_name":"SessionStart","session_id":"sess1234"}`,
		``,
		`{"hook_event_name":"SomeFutureEvent","session_id":"sess1234"}`,
	} {
		capture(t, evJSON, func() int { return CheckInbox(nil) })
		if countInbox(p, "bob") != 1 {
			t.Fatalf("event %q must NOT consume", evJSON)
		}
	}
	// the real prompt delivers
	out, _ := capture(t, ev("sess1234", ""), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "precieux") || countInbox(p, "bob") != 0 {
		t.Fatal("UserPromptSubmit must deliver and consume")
	}
	// the delivery journal recorded the consuming run
	b, err := os.ReadFile(filepath.Join(p.State(), "delivery.log"))
	if err != nil || !strings.Contains(string(b), `"sid":"sess1234"`) || !strings.Contains(string(b), "msg-") {
		t.Errorf("delivery.log must record who consumed what: %v %s", err, b)
	}
}

// --- priority flag ------------------------------------------------------------

// captureStderr swaps os.Stderr for the duration of fn (stdout/stdin untouched).
func captureStderr(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stderr
	defer func() { os.Stderr = old }()
	r, w, _ := os.Pipe()
	os.Stderr = w
	done := make(chan string, 1)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	code := fn()
	w.Close()
	out := <-done
	return out, code
}

// inboxContains reports whether any pending message of peer contains want.
func inboxContains(t *testing.T, p store.Paths, peer, want string) bool {
	t.Helper()
	entries, _ := os.ReadDir(p.Inbox(peer))
	for _, e := range entries {
		if !isMsg(e.Name()) {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(p.Inbox(peer), e.Name()))
		if strings.Contains(string(b), want) {
			return true
		}
	}
	return false
}

func TestSendPriorityFlag(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	initPeer(t, "bob")

	// -p urgent is written into the message
	if _, code := capture(t, "", func() int {
		return Send([]string{"-f", "alice", "-p", "urgent", "bob", "fire"})
	}); code != 0 {
		t.Fatalf("send -p urgent exit %d", code)
	}
	if !inboxContains(t, p, "bob", `"priority":"urgent"`) {
		t.Error("-p urgent must set priority:urgent in the message")
	}

	// -p normal is accepted (explicit default)
	if _, code := capture(t, "", func() int {
		return Send([]string{"-f", "alice", "-p", "normal", "bob", "calm"})
	}); code != 0 {
		t.Error("send -p normal must be accepted")
	}

	// anything else is a usage error, nothing written
	before := countInbox(p, "bob")
	if _, code := capture(t, "", func() int {
		return Send([]string{"-f", "alice", "-p", "high", "bob", "nope"})
	}); code != 1 {
		t.Error("send -p high must exit 1 (usage error)")
	}
	if _, code := capture(t, "", func() int {
		return Send([]string{"-f", "alice", "--priority", "bob", "nope"})
	}); code != 1 {
		t.Error("--priority swallowing the peer as its value must fail, not send")
	}
	if countInbox(p, "bob") != before {
		t.Error("a refused -p value must not write a message")
	}
}

func TestBroadcastPriorityFlag(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	initPeer(t, "bob")
	initPeer(t, "carol")
	if _, code := capture(t, "", func() int {
		return Broadcast([]string{"-f", "alice", "-p", "urgent", "all hands"})
	}); code != 0 {
		t.Fatal("broadcast -p urgent must succeed")
	}
	for _, peer := range []string{"bob", "carol"} {
		if !inboxContains(t, p, peer, `"priority":"urgent"`) {
			t.Errorf("broadcast -p urgent must reach %s as urgent", peer)
		}
	}
	if _, code := capture(t, "", func() int {
		return Broadcast([]string{"-f", "alice", "-p", "asap", "x"})
	}); code != 1 {
		t.Error("broadcast -p asap must exit 1")
	}
}

func TestSpoolDepositUrgentPrioritySurvivesParse(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "bob")
	initPeer(t, "remote-agent")
	_, code := spoolDep(t, "remote-agent",
		`{"id":"msg-7-ee","from":"remote-agent","to":"bob","content":"now","priority":"urgent"}`)
	if code != 0 {
		t.Fatalf("urgent deposit must succeed, got %d", code)
	}
	b, err := os.ReadFile(filepath.Join(p.Inbox("bob"), "msg-7-ee.json"))
	if err != nil {
		t.Fatalf("deposited message unreadable: %v", err)
	}
	m, err := msg.Parse(b)
	if err != nil {
		t.Fatalf("deposited message must stay parseable: %v", err)
	}
	if m.Priority != "urgent" {
		t.Errorf("priority must survive deposit+parse, got %q", m.Priority)
	}
}

// --- pair circuit breaker -----------------------------------------------------

// breakerEnv shrinks the thresholds and disables the dedup window so repeated
// test sends hit the breaker, not the anti-duplicate ledger.
func breakerEnv(t *testing.T, soft, hard int) {
	t.Helper()
	t.Setenv("WHISPER_DUP_WINDOW", "0")
	t.Setenv("WHISPER_PAIR_SOFT", strconv.Itoa(soft))
	t.Setenv("WHISPER_PAIR_HARD", strconv.Itoa(hard))
	t.Setenv("WHISPER_PAIR_WINDOW", "600")
}

func pairSend(t *testing.T, from, to, content string) (string, int) {
	t.Helper()
	var stderr string
	var code int
	capture(t, "", func() int {
		stderr, code = captureStderr(t, func() int {
			return Send([]string{"-f", from, to, content})
		})
		return code
	})
	return stderr, code
}

func TestPairBreakerSoftWarnsHardRefuses(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	initPeer(t, "bob")
	initPeer(t, "carol")
	breakerEnv(t, 2, 4)

	// sends 1-2 (0 and 1 prior in window): silent. Directions alternate — the
	// pair is UNORDERED, a ping-pong loop counts on one window.
	if errOut, code := pairSend(t, "alice", "bob", "m1"); code != 0 || strings.Contains(errOut, "possible loop") {
		t.Fatalf("send 1 must pass silently: code=%d err=%q", code, errOut)
	}
	if errOut, code := pairSend(t, "bob", "alice", "m2"); code != 0 || strings.Contains(errOut, "possible loop") {
		t.Fatalf("send 2 must pass silently: code=%d err=%q", code, errOut)
	}
	// sends 3-4 (2 and 3 prior ≥ soft): delivered, loud warning
	for i, dir := range []struct{ from, to string }{{"alice", "bob"}, {"bob", "alice"}} {
		errOut, code := pairSend(t, dir.from, dir.to, "m"+strconv.Itoa(3+i))
		if code != 0 {
			t.Fatalf("soft-zone send %d must still be delivered, got exit %d", 3+i, code)
		}
		if !strings.Contains(errOut, "possible loop") || !strings.Contains(errOut, "LLM agent") {
			t.Errorf("soft-zone send %d must warn an LLM sender loudly, got %q", 3+i, errOut)
		}
	}
	// send 5 (4 prior ≥ hard): REFUSED + sentinel
	errOut, code := pairSend(t, "alice", "bob", "m5")
	if code != 1 {
		t.Fatalf("hard-zone send must be refused (exit 1), got %d", code)
	}
	if !strings.Contains(errOut, "REFUSED") || !strings.Contains(errOut, "human") {
		t.Errorf("refusal must tell the sender to report to a human, got %q", errOut)
	}
	if inboxContains(t, p, "bob", "m5") {
		t.Error("refused send must not write a message")
	}
	sentinel := filepath.Join(p.Warnings(), "pair-flood-alice-bob.warn")
	b, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("hard refusal must write pair-flood-alice-bob.warn: %v", err)
	}
	for _, want := range []string{`"pair":"alice|bob"`, `"count":4`, `"window":600`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("sentinel payload missing %s: %s", want, b)
		}
	}

	// other pairs are unaffected
	if _, code := pairSend(t, "alice", "carol", "hello carol"); code != 0 {
		t.Error("an unrelated pair must not be blocked")
	}
	// broadcast counts per-recipient pair: alice→bob still blocked, alice→carol delivered
	carolBefore, bobBefore := countInbox(p, "carol"), countInbox(p, "bob")
	capture(t, "", func() int {
		_, c := captureStderr(t, func() int { return Broadcast([]string{"-f", "alice", "bcast"}) })
		return c
	})
	if countInbox(p, "bob") != bobBefore {
		t.Error("broadcast must not bypass the breaker on the flooded pair")
	}
	if countInbox(p, "carol") != carolBefore+1 {
		t.Error("broadcast must still reach unflooded pairs")
	}
}

func TestPairBreakerWindowSlidesAndSentinelClears(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	initPeer(t, "bob")
	breakerEnv(t, 2, 3)

	for i := 0; i < 3; i++ {
		if _, code := pairSend(t, "alice", "bob", "w"+strconv.Itoa(i)); code != 0 {
			t.Fatalf("send %d under hard limit must pass, got %d", i, code)
		}
	}
	if _, code := pairSend(t, "alice", "bob", "w-over"); code != 1 {
		t.Fatal("4th send must trip the hard limit")
	}
	sentinel := filepath.Join(p.Warnings(), "pair-flood-alice-bob.warn")
	if !fileExists(sentinel) {
		t.Fatal("tripped breaker must leave a pair-flood sentinel")
	}

	// Slide the window: age every ledger entry past WHISPER_PAIR_WINDOW.
	raw, err := os.ReadFile(p.PairLedger())
	if err != nil {
		t.Fatalf("pair ledger must exist: %v", err)
	}
	var l struct {
		Entries []struct {
			TS   int64  `json:"ts"`
			Pair string `json:"pair"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatalf("pair ledger must be valid JSON: %v", err)
	}
	old := time.Now().Unix() - 700
	for i := range l.Entries {
		l.Entries[i].TS = old
	}
	b, _ := json.Marshal(l)
	if err := os.WriteFile(p.PairLedger(), b, 0o600); err != nil {
		t.Fatal(err)
	}

	// Window drained: the send passes, the resolved sentinel is auto-cleared,
	// and the stale entries were pruned on read.
	errOut, code := pairSend(t, "alice", "bob", "after-drain")
	if code != 0 || strings.Contains(errOut, "possible loop") {
		t.Fatalf("post-drain send must pass silently: code=%d err=%q", code, errOut)
	}
	if fileExists(sentinel) {
		t.Error("a send back under the soft threshold must clear the pair-flood sentinel")
	}
	raw, _ = os.ReadFile(p.PairLedger())
	if strings.Count(string(raw), `"pair"`) != 1 {
		t.Errorf("stale entries must be pruned as the ledger is read: %s", raw)
	}
}

func TestPairBreakerCorruptLedgerFailsOpen(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	initPeer(t, "bob")
	breakerEnv(t, 2, 3)
	if err := os.MkdirAll(p.State(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.PairLedger(), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	// a bookkeeping bug must never lose a legitimate send
	if _, code := pairSend(t, "alice", "bob", "still-goes"); code != 0 {
		t.Fatal("corrupt ledger must fail-open: the send goes through")
	}
	if !inboxContains(t, p, "bob", "still-goes") {
		t.Error("message must be delivered despite the corrupt ledger")
	}
	raw, err := os.ReadFile(p.PairLedger())
	if err != nil || !strings.Contains(string(raw), `"entries"`) {
		t.Errorf("corrupt ledger must be rewritten fresh: %v %s", err, raw)
	}
	// the rebuilt ledger enforces again: flood it and expect a refusal
	for i := 0; i < 3; i++ {
		pairSend(t, "alice", "bob", "f"+strconv.Itoa(i))
	}
	if _, code := pairSend(t, "alice", "bob", "over"); code != 1 {
		t.Error("the check must resume on the rebuilt ledger (fail-open is for reading only)")
	}
}

// --- unrouted / unrouted-pending sentinel lifecycle ---------------------------

func TestUnroutedPendingClearedOnClaim(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "SEO", "bob", "pending-then-claimed"}) })

	// a consuming session that does NOT carry the title observes that nobody
	// live does → unrouted-pending sentinel appears
	capture(t, ev("sessDEV12", "DEV"), func() int { return CheckInbox(nil) })
	warns, _ := filepath.Glob(filepath.Join(p.Warnings(), "unrouted-pending-*.warn"))
	if len(warns) != 1 {
		t.Fatalf("an unroutable title must raise unrouted-pending, found %d", len(warns))
	}

	// the matching session shows up and claims → sentinel cleared
	out, _ := capture(t, ev("sessSEO12", "SEO"), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "pending-then-claimed") {
		t.Fatal("matching session must receive the message")
	}
	warns, _ = filepath.Glob(filepath.Join(p.Warnings(), "unrouted-pending-*.warn"))
	if len(warns) != 0 {
		t.Error("a claimed message must clear its unrouted-pending sentinel")
	}
}

func TestUnroutedPendingClearedOnReidentification(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	// session lives as "Old", then renames to "New" (rename observed now)
	capture(t, ev("sessRENB1", "Old"), func() int { return CheckInbox(nil) })
	capture(t, ev("sessRENB1", "New"), func() int { return CheckInbox(nil) })

	// a message targeting "Old" whose mtime predates the rename observation
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "Old", "bob", "pre-rename-pending"}) })
	entries, _ := os.ReadDir(p.Inbox("bob"))
	f := filepath.Join(p.Inbox("bob"), entries[0].Name())
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(f, past, past)

	// an unrelated session sees no live "Old" → pending sentinel raised
	capture(t, ev("sessOTHER1", "X"), func() int { return CheckInbox(nil) })
	warns, _ := filepath.Glob(filepath.Join(p.Warnings(), "unrouted-pending-*.warn"))
	if len(warns) != 1 {
		t.Fatalf("expected one unrouted-pending sentinel, found %d", len(warns))
	}

	// the renamed session re-identifies and claims → sentinel cleared
	out, _ := capture(t, ev("sessRENB1", "New"), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "pre-rename-pending") {
		t.Fatal("re-identification must deliver the pre-rename message")
	}
	warns, _ = filepath.Glob(filepath.Join(p.Warnings(), "unrouted-pending-*.warn"))
	if len(warns) != 0 {
		t.Error("a re-identified claim must clear the unrouted-pending sentinel")
	}
}

func TestUnroutedPendingClearedOnEscalation(t *testing.T) {
	p := sandbox(t)
	initPeer(t, "alice")
	proj := initPeer(t, "bob")
	t.Chdir(proj)
	capture(t, "", func() int { return Send([]string{"-f", "alice", "-s", "Ghost", "bob", "will-escalate"}) })

	// pending state first (nobody carries "Ghost")
	capture(t, ev("sessWAIT1", "Other"), func() int { return CheckInbox(nil) })
	warns, _ := filepath.Glob(filepath.Join(p.Warnings(), "unrouted-pending-*.warn"))
	if len(warns) != 1 {
		t.Fatalf("expected one unrouted-pending sentinel, found %d", len(warns))
	}

	// then the timeout passes and the message escalates
	t.Setenv("WHISPER_ROUTE_TIMEOUT", "0")
	entries, _ := os.ReadDir(p.Inbox("bob"))
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(filepath.Join(p.Inbox("bob"), entries[0].Name()), old, old)
	out, _ := capture(t, ev("sessWAIT1", "Other"), func() int { return CheckInbox(nil) })
	if !strings.Contains(out, "undelivered") {
		t.Fatal("escalation banner expected")
	}
	warns, _ = filepath.Glob(filepath.Join(p.Warnings(), "unrouted-pending-*.warn"))
	if len(warns) != 0 {
		t.Error("escalation must clear the unrouted-pending sentinel")
	}
	warns, _ = filepath.Glob(filepath.Join(p.Warnings(), "unrouted-*.warn"))
	if len(warns) != 0 {
		t.Error("a rendered+journaled escalation must clear its unrouted sentinel too")
	}
}
