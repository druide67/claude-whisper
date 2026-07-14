// Package store owns the on-disk layout of ~/.claude-whisper and every write
// into it. All writes are atomic (.tmp + rename) with fixed perms: the root
// and directories are 0700, files 0600 — the single place that convention is
// enforced, instead of being re-typed in every bash script.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const (
	DirPerm  = 0o700
	FilePerm = 0o600
)

// Paths resolves every location under the whisper root.
type Paths struct{ Root string }

// New resolves the root from $WHISPER_DIR, falling back to ~/.claude-whisper.
func New() Paths {
	if d := os.Getenv("WHISPER_DIR"); d != "" {
		return Paths{Root: d}
	}
	home, _ := os.UserHomeDir()
	return Paths{Root: filepath.Join(home, ".claude-whisper")}
}

func (p Paths) Inbox(peer string) string { return filepath.Join(p.Root, "inbox", peer) }
func (p Paths) Archive() string          { return filepath.Join(p.Root, "archive") }
func (p Paths) State() string            { return filepath.Join(p.Root, "state") }
func (p Paths) Warnings() string         { return filepath.Join(p.Root, "state", "warnings") }
func (p Paths) PeersFile() string        { return filepath.Join(p.Root, "peers.json") }
func (p Paths) ConfigFile() string       { return filepath.Join(p.Root, "state", "config.json") }
func (p Paths) RecentSends() string      { return filepath.Join(p.Root, "state", "recent-sends.json") }
func (p Paths) MultiState(peer string) string {
	return filepath.Join(p.Root, "state", "multi", peer+".json")
}

// EnsureDir creates dir (and parents) with 0700.
func EnsureDir(dir string) error { return os.MkdirAll(dir, DirPerm) }

// AtomicWrite writes data to path via a UNIQUE sibling temp file then rename,
// chmod 0600. The temp name is unique (os.CreateTemp) so two concurrent writers
// to the same target never share a temp and can't produce a torn file.
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, FilePerm); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Lock takes an exclusive advisory lock (flock) tied to target, via a dedicated
// <target>.lock file that is never renamed (so the lock survives the atomic
// rename of the target itself). Returns a release func. Used to serialize the
// read-modify-write of shared state files across concurrent processes — the
// atomic rename alone prevents torn reads but NOT lost updates.
func Lock(target string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(target), DirPerm); err != nil {
		return func() {}, err
	}
	f, err := os.OpenFile(target+".lock", os.O_CREATE|os.O_RDWR, FilePerm)
	if err != nil {
		return func() {}, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return func() {}, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// AtomicWriteJSON marshals v compactly (stable key order) and atomically writes.
func AtomicWriteJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(b, '\n'))
}

// --- peers.json -------------------------------------------------------------
// Modeled as a generic map so unknown/future fields (e.g. transport) survive an
// upsert untouched — mirroring what jq's targeted assignments did in bash.

type PeersFile struct {
	Peers map[string]map[string]any `json:"peers"`
}

func (p Paths) ReadPeers() (*PeersFile, error) {
	b, err := os.ReadFile(p.PeersFile())
	if err != nil {
		if os.IsNotExist(err) {
			return &PeersFile{Peers: map[string]map[string]any{}}, nil
		}
		return nil, err
	}
	var pf PeersFile
	if err := json.Unmarshal(b, &pf); err != nil {
		return nil, err
	}
	if pf.Peers == nil {
		pf.Peers = map[string]map[string]any{}
	}
	return &pf, nil
}

func (pf *PeersFile) entry(id string) map[string]any {
	if pf.Peers[id] == nil {
		pf.Peers[id] = map[string]any{}
	}
	return pf.Peers[id]
}

// IsRegistered reports whether id has a peers.json entry.
func (pf *PeersFile) IsRegistered(id string) bool { _, ok := pf.Peers[id]; return ok }

// RegisterLocal upserts a local (project) peer: registered set once, last_seen
// refreshed, cwd recorded.
func (pf *PeersFile) RegisterLocal(id, cwd string, now time.Time) {
	e := pf.entry(id)
	if _, ok := e["registered"]; !ok {
		e["registered"] = isoUTC(now)
	}
	e["last_seen"] = isoUTC(now)
	e["cwd"] = cwd
}

// RegisterTransport upserts a remote/transport peer: a transport pointer only
// (never a key or address), and no cwd (it has no local project).
func (pf *PeersFile) RegisterTransport(id, ttype, sshAlias, keyPath string, now time.Time) {
	e := pf.entry(id)
	if _, ok := e["registered"]; !ok {
		e["registered"] = isoUTC(now)
	}
	tr := map[string]any{"type": ttype}
	if sshAlias != "" {
		tr["ssh_alias"] = sshAlias
	}
	if keyPath != "" {
		tr["key_path"] = keyPath
	}
	e["transport"] = tr
	delete(e, "cwd")
}

// TouchLastSeen refreshes last_seen for id (creating a minimal entry if needed).
func (pf *PeersFile) TouchLastSeen(id string, now time.Time) {
	pf.entry(id)["last_seen"] = isoUTC(now)
}

// SortedIDs returns the peer-ids in stable order (deterministic output).
func (pf *PeersFile) SortedIDs() []string {
	ids := make([]string, 0, len(pf.Peers))
	for id := range pf.Peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (p Paths) WritePeers(pf *PeersFile) error { return AtomicWriteJSON(p.PeersFile(), pf) }

// UpdatePeers runs a locked read-modify-write on peers.json: it takes the
// peers.json lock, re-reads the current registry, applies mut, and writes it
// back — so a concurrent init/rehome/heartbeat can never clobber another's
// change (lost update). All peers.json mutators must go through this.
func (p Paths) UpdatePeers(mut func(*PeersFile)) error {
	release, err := Lock(p.PeersFile())
	if err != nil {
		return err
	}
	defer release()
	pf, err := p.ReadPeers()
	if err != nil {
		return err
	}
	mut(pf)
	return p.WritePeers(pf)
}

// --- config.json ------------------------------------------------------------

type Config struct {
	AutoGlobal  string            `json:"autoGlobal"`
	ModePerPeer map[string]string `json:"modePerPeer"`
	PauseUntil  string            `json:"pauseUntil,omitempty"`
}

// ReadConfig returns the delivery config, or the default (autoGlobal=on) when
// the file is absent or unreadable — matching config.ts DEFAULT_SHARED_CONFIG.
func (p Paths) ReadConfig() Config {
	def := Config{AutoGlobal: "on", ModePerPeer: map[string]string{}}
	b, err := os.ReadFile(p.ConfigFile())
	if err != nil {
		return def
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return def
	}
	if c.AutoGlobal == "" {
		c.AutoGlobal = "on"
	}
	if c.ModePerPeer == nil {
		c.ModePerPeer = map[string]string{}
	}
	return c
}

// EffectiveMode mirrors config.ts effectiveMode: off/paused are fail-closed to
// "off"; otherwise the per-peer mode, defaulting to "notify" for unlisted peers.
func (c Config) EffectiveMode(peer string) string {
	switch c.AutoGlobal {
	case "off", "paused":
		return "off"
	}
	if m, ok := c.ModePerPeer[peer]; ok {
		return m
	}
	return "notify"
}

func isoUTC(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }
