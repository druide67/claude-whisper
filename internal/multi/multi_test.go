package multi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHeartbeatTracksRenames(t *testing.T) {
	s := newState()
	s.Heartbeat("sess1234", "dev", 100)
	e := s.Sessions["sess1234"]
	if e.Title != "dev" || e.PrevTitle != "" {
		t.Fatalf("first title: %+v", e)
	}
	s.Heartbeat("sess1234", "dev", 200) // same title → no rename recorded
	if e.PrevTitle != "" || e.RenamedAt != 0 {
		t.Errorf("no-op heartbeat must not record a rename: %+v", e)
	}
	s.Heartbeat("sess1234", "seo", 300) // rename dev→seo
	if e.Title != "seo" || e.PrevTitle != "dev" || e.RenamedAt != 300 {
		t.Errorf("rename not tracked: %+v", e)
	}
	// anonymous → titled is NOT a rename (there was no previous address)
	s2 := newState()
	s2.Heartbeat("sessABCD", "", 100)
	s2.Heartbeat("sessABCD", "dev", 200)
	if s2.Sessions["sessABCD"].PrevTitle != "" {
		t.Error("anonymous→titled must not set PrevTitle")
	}
}

func TestLiveTitlesAndAnyLiveTitled(t *testing.T) {
	s := newState()
	s.Heartbeat("a1234567", "dev", 1000)
	s.Heartbeat("b1234567", "seo", 1000)
	s.Heartbeat("c1234567", "", 1000)   // anonymous
	s.Heartbeat("d1234567", "dev", 100) // stale duplicate title
	titles, anon := s.LiveTitles(1000, 60)
	if len(titles) != 2 || anon != 1 {
		t.Errorf("LiveTitles = %v anon=%d, want 2 titles + 1 anonymous", titles, anon)
	}
	if !s.AnyLiveTitled("seo", 1000, 60) || s.AnyLiveTitled("ghost", 1000, 60) {
		t.Error("AnyLiveTitled wrong")
	}
	if s.AnyLiveTitled("dev", 5000, 60) {
		t.Error("stale sessions must not count as live")
	}
}

func TestGCReturnsDroppedAndCleansSeen(t *testing.T) {
	s := newState()
	s.Heartbeat("live1234", "x", 1000)
	s.Heartbeat("dead1234", "y", 100)
	s.MarkSeen("msg-1-aa", "dead1234")
	s.MarkSeen("msg-1-aa", "live1234")
	dropped := s.GC(1000, 60)
	if len(dropped) != 1 || dropped[0] != "dead1234" {
		t.Fatalf("dropped = %v", dropped)
	}
	if s.HasSeen("msg-1-aa", "dead1234") {
		t.Error("dead session must be removed from seen-sets")
	}
	if !s.AllLiveSeen("msg-1-aa", 1000, 60) {
		t.Error("remaining live session saw it → archivable")
	}
}

func TestAllLiveSeenNoLiveSession(t *testing.T) {
	s := newState()
	if s.AllLiveSeen("msg-1-aa", 1000, 60) {
		t.Error("no live session → message must wait, not archive")
	}
}

func TestPruneSeen(t *testing.T) {
	s := newState()
	s.MarkSeen("msg-1-aa", "a1234567")
	s.MarkSeen("msg-2-bb", "a1234567")
	s.PruneSeen(func(id string) bool { return id == "msg-2-bb" })
	if _, ok := s.Seen["msg-1-aa"]; ok {
		t.Error("orphan seen-entry must be pruned")
	}
	if _, ok := s.Seen["msg-2-bb"]; !ok {
		t.Error("existing message's entry must survive")
	}
}

func TestLoadSaveRoundTripAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peer.json")
	s := newState()
	s.Heartbeat("a1234567", "dev", 42)
	s.MarkSeen("msg-1-aa", "a1234567")
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if got.Sessions["a1234567"].Title != "dev" || got.Sessions["a1234567"].LastSeen != 42 {
		t.Errorf("roundtrip lost data: %+v", got.Sessions["a1234567"])
	}
	if !got.HasSeen("msg-1-aa", "a1234567") {
		t.Error("seen lost in roundtrip")
	}
	// corrupt / legacy-format file → fresh state, never a crash
	_ = os.WriteFile(path, []byte(`{"sessions":{"a1234567":12345}}`), 0o600)
	if fresh := Load(path); len(fresh.Sessions) != 0 {
		t.Error("unparseable/legacy format must yield a fresh state")
	}
}
