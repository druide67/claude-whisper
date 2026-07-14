package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	warns, _ := filepath.Glob(filepath.Join(p.Warnings(), "unrouted-*.warn"))
	if len(warns) == 0 {
		t.Error("escalation must leave an unrouted sentinel")
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
