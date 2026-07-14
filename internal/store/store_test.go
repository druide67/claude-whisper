package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func tmpPaths(t *testing.T) Paths {
	t.Helper()
	return Paths{Root: t.TempDir()}
}

func TestAtomicWritePerms(t *testing.T) {
	p := tmpPaths(t)
	target := filepath.Join(p.Root, "sub", "x.json")
	if err := AtomicWrite(target, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != FilePerm {
		t.Errorf("file perm = %o, want %o", fi.Mode().Perm(), FilePerm)
	}
	// no .tmp left behind
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Error(".tmp left behind")
	}
}

func TestPeersUpsertPreservesUnknownFields(t *testing.T) {
	p := tmpPaths(t)
	// seed a peers.json with an unknown/future field on an existing peer
	seed := `{"peers":{"frontend":{"registered":"2026-01-01T00:00:00Z","cwd":"/x","future_flag":true}}}`
	if err := AtomicWrite(p.PeersFile(), []byte(seed)); err != nil {
		t.Fatal(err)
	}
	pf, err := p.ReadPeers()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	pf.TouchLastSeen("frontend", now)
	pf.RegisterLocal("backend", "/y", now)
	if err := p.WritePeers(pf); err != nil {
		t.Fatal(err)
	}

	got, _ := p.ReadPeers()
	// unknown field survived
	if got.Peers["frontend"]["future_flag"] != true {
		t.Error("unknown field future_flag was dropped on upsert")
	}
	// registered not overwritten
	if got.Peers["frontend"]["registered"] != "2026-01-01T00:00:00Z" {
		t.Error("registered should be preserved (//=)")
	}
	// last_seen refreshed
	if got.Peers["frontend"]["last_seen"] != "2026-07-11T00:00:00Z" {
		t.Errorf("last_seen not refreshed: %v", got.Peers["frontend"]["last_seen"])
	}
	if !got.IsRegistered("backend") {
		t.Error("backend should be registered")
	}
}

func TestRegisterTransportNoCwdNoSecret(t *testing.T) {
	p := tmpPaths(t)
	pf, _ := p.ReadPeers()
	now := time.Now()
	pf.RegisterLocal("remote-agent", "/tmp/x", now) // pretend it had a cwd
	pf.RegisterTransport("remote-agent", "ssh-spool", "", "", now)
	_ = p.WritePeers(pf)

	got, _ := p.ReadPeers()
	e := got.Peers["remote-agent"]
	if _, hasCwd := e["cwd"]; hasCwd {
		t.Error("transport peer must not keep cwd")
	}
	tr, ok := e["transport"].(map[string]any)
	if !ok || tr["type"] != "ssh-spool" {
		t.Errorf("transport pointer missing/wrong: %v", e["transport"])
	}
	if _, hasAlias := tr["ssh_alias"]; hasAlias {
		t.Error("empty ssh_alias must not be written")
	}
}

func TestEffectiveMode(t *testing.T) {
	c := Config{AutoGlobal: "on", ModePerPeer: map[string]string{"a": "inject", "b": "off"}}
	cases := map[string]string{"a": "inject", "b": "off", "unlisted": "notify"}
	for peer, want := range cases {
		if got := c.EffectiveMode(peer); got != want {
			t.Errorf("EffectiveMode(%q)=%q want %q", peer, got, want)
		}
	}
	// paused + off are fail-closed regardless of per-peer mode
	for _, auto := range []string{"off", "paused"} {
		c.AutoGlobal = auto
		if got := c.EffectiveMode("a"); got != "off" {
			t.Errorf("autoGlobal=%s EffectiveMode(a)=%q want off", auto, got)
		}
	}
}

func TestReadConfigDefault(t *testing.T) {
	p := tmpPaths(t)
	c := p.ReadConfig() // no file
	if c.AutoGlobal != "on" {
		t.Errorf("default autoGlobal = %q, want on", c.AutoGlobal)
	}
	// malformed → default
	_ = AtomicWrite(p.ConfigFile(), []byte("{not json"))
	if p.ReadConfig().AutoGlobal != "on" {
		t.Error("malformed config should fall back to default")
	}
}

func TestUpdatePeersConcurrentNoLostUpdate(t *testing.T) {
	p := tmpPaths(t)
	// seed the file so all writers read-modify-write the same target
	if err := p.WritePeers(&PeersFile{Peers: map[string]map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("peer%02d", i)
			_ = p.UpdatePeers(func(pf *PeersFile) { pf.RegisterLocal(id, "/x", time.Unix(1, 0)) })
		}(i)
	}
	wg.Wait()
	got, _ := p.ReadPeers()
	if len(got.Peers) != n {
		t.Fatalf("lost update under concurrency: got %d peers, want %d", len(got.Peers), n)
	}
}

func TestAtomicWriteConcurrentSameTargetStaysValid(t *testing.T) {
	p := tmpPaths(t)
	target := filepath.Join(p.Root, "x.json")
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = AtomicWrite(target, []byte(fmt.Sprintf(`{"v":%d}`, i)))
		}(i)
	}
	wg.Wait()
	// file must be valid JSON (no torn write) and no stray temp files left
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("torn write: %v (%q)", err, b)
	}
	entries, _ := os.ReadDir(p.Root)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("stray temp file left: %s", e.Name())
		}
	}
}

func TestReadPeersRoundTripValidJSON(t *testing.T) {
	p := tmpPaths(t)
	pf, _ := p.ReadPeers()
	pf.RegisterLocal("frontend", "/x", time.Now())
	_ = p.WritePeers(pf)
	// output must be valid JSON with a peers object
	b, _ := os.ReadFile(p.PeersFile())
	var check map[string]any
	if err := json.Unmarshal(b, &check); err != nil {
		t.Fatalf("peers.json not valid JSON: %v", err)
	}
	if _, ok := check["peers"]; !ok {
		t.Error("peers.json missing 'peers' key")
	}
}
