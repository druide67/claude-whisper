package msg

import (
	"strings"
	"testing"
	"time"
)

func TestNewID(t *testing.T) {
	got := NewID(1779996645, []byte{0xbe, 0x50, 0x63, 0x0d})
	want := "msg-1779996645-be50630d"
	if got != want {
		t.Errorf("NewID = %q, want %q", got, want)
	}
}

func TestFormatTimestamp(t *testing.T) {
	tm := time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC)
	if got := FormatTimestamp(tm); got != "2026-04-03T14:00:00Z" {
		t.Errorf("FormatTimestamp = %q", got)
	}
}

func TestMarshalSchema(t *testing.T) {
	m := &Message{
		ID: "msg-1-ab", From: "alice", To: "bob",
		Timestamp: "2026-04-03T14:00:00Z", Content: "hi",
		Priority: "normal", TTL: 3600, HopCount: 0,
	}
	b, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// Field order + presence, and no optional keys when empty.
	want := `{"id":"msg-1-ab","from":"alice","to":"bob","timestamp":"2026-04-03T14:00:00Z","content":"hi","priority":"normal","ttl":3600,"hop_count":0}` + "\n"
	if s != want {
		t.Errorf("Marshal mismatch:\n got=%s\nwant=%s", s, want)
	}
}

func TestMarshalOptionalFields(t *testing.T) {
	m := &Message{ID: "msg-1-ab", From: "a", To: "b", Timestamp: "t", Content: "c", Priority: "normal", TTL: 3600, HopCount: 2, Thread: "auth", InReplyTo: "msg-0-aa"}
	b, _ := Marshal(m)
	s := string(b)
	if !strings.Contains(s, `"thread":"auth"`) || !strings.Contains(s, `"in_reply_to":"msg-0-aa"`) {
		t.Errorf("optional fields missing: %s", s)
	}
}

func TestParseDefaults(t *testing.T) {
	// legacy/hand-written file without priority/ttl, plus an unknown field
	m, err := Parse([]byte(`{"id":"msg-1-ab","from":"a","to":"b","timestamp":"t","content":"c","legacy_extra":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Priority != "normal" || m.TTL != 3600 {
		t.Errorf("defaults not applied: priority=%q ttl=%d", m.Priority, m.TTL)
	}
}

func TestParseClampsHostileNumerics(t *testing.T) {
	// A crafted negative hop_count (e.g. via a transport deposit) must not
	// undermine loop detection; a negative ttl falls back to the default.
	m, err := Parse([]byte(`{"id":"msg-1-ab","from":"a","to":"b","timestamp":"t","content":"c","hop_count":-5,"ttl":-1}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.HopCount != 0 {
		t.Errorf("negative hop_count must clamp to 0, got %d", m.HopCount)
	}
	if m.TTL != 3600 {
		t.Errorf("negative ttl must clamp to default, got %d", m.TTL)
	}
}
