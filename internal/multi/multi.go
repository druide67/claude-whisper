// Package multi tracks the sessions of a peer: heartbeats, session
// titles (the addressing identity), and — for broadcast ('*') messages only —
// which sessions have rendered which message.
//
// The whole thing is ONE JSON file per peer (state/multi/<peer>.json) with real
// maps, read once, mutated in memory, written once, always under the per-peer
// flock held by the hook.
package multi

import (
	"encoding/json"
	"os"

	"github.com/druide67/claude-whisper/internal/store"
)

// Session is one live (or recently live) conversation of the peer.
type Session struct {
	LastSeen int64 `json:"last_seen"`
	// Title is the last session_title observed for this sid ("" = anonymous).
	Title string `json:"title,omitempty"`
	// PrevTitle/RenamedAt support re-identification: when a rename is
	// observed, the old title and the observation instant are kept so a
	// message sent to the OLD title BEFORE the rename can still find me.
	PrevTitle string `json:"prev_title,omitempty"`
	RenamedAt int64  `json:"renamed_at,omitempty"`
}

// State is the per-peer session registry + broadcast journal.
type State struct {
	Sessions map[string]*Session `json:"sessions"`
	// Seen: message-id -> sids that rendered it. Only used for '*' messages.
	Seen map[string][]string `json:"seen"`
}

func newState() *State {
	return &State{Sessions: map[string]*Session{}, Seen: map[string][]string{}}
}

// Load reads the peer's state, or an empty one if absent/corrupt (the file is
// a registry, never a delivery authority — starting fresh is always safe).
func Load(path string) *State {
	b, err := os.ReadFile(path)
	if err != nil {
		return newState()
	}
	var s State
	if json.Unmarshal(b, &s) != nil {
		return newState()
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*Session{}
	}
	for sid, e := range s.Sessions {
		if e == nil {
			delete(s.Sessions, sid)
		}
	}
	if s.Seen == nil {
		s.Seen = map[string][]string{}
	}
	return &s
}

// Save atomically persists the state.
func Save(path string, s *State) error { return store.AtomicWriteJSON(path, s) }

// Heartbeat records that sid is alive at now with the given title, tracking
// renames (title change → PrevTitle/RenamedAt updated).
func (s *State) Heartbeat(sid, title string, now int64) {
	e := s.Sessions[sid]
	if e == nil {
		e = &Session{}
		s.Sessions[sid] = e
	}
	if title != e.Title {
		if e.Title != "" {
			e.PrevTitle = e.Title
			e.RenamedAt = now
		}
		e.Title = title
	}
	e.LastSeen = now
}

// Live reports whether sid heartbeated within grace.
func (s *State) Live(sid string, now, grace int64) bool {
	e := s.Sessions[sid]
	return e != nil && now-e.LastSeen <= grace
}

// LiveTitles returns the set of titles carried by live sessions, plus the
// count of live anonymous sessions.
func (s *State) LiveTitles(now, grace int64) (titles []string, anonymous int) {
	seen := map[string]bool{}
	for _, e := range s.Sessions {
		if now-e.LastSeen > grace {
			continue
		}
		if e.Title == "" {
			anonymous++
		} else if !seen[e.Title] {
			seen[e.Title] = true
			titles = append(titles, e.Title)
		}
	}
	return titles, anonymous
}

// AnyLiveTitled reports whether a live session currently carries title.
func (s *State) AnyLiveTitled(title string, now, grace int64) bool {
	for _, e := range s.Sessions {
		if now-e.LastSeen <= grace && e.Title == title {
			return true
		}
	}
	return false
}

// GC drops sessions whose heartbeat is older than grace, and removes them from
// every message's seen-set (so a dead session never blocks archival forever).
// Returns the sids dropped (their pending claims must be re-inboxed by the
// caller).
func (s *State) GC(now, grace int64) (dropped []string) {
	for sid, e := range s.Sessions {
		if now-e.LastSeen > grace {
			delete(s.Sessions, sid)
			dropped = append(dropped, sid)
			for id, seers := range s.Seen {
				s.Seen[id] = remove(seers, sid)
				if len(s.Seen[id]) == 0 {
					delete(s.Seen, id)
				}
			}
		}
	}
	return dropped
}

// PruneSeen drops seen-entries whose message no longer exists (exists reports
// whether the message id is still pending) — a lost archive race must not
// leave immortal entries behind.
func (s *State) PruneSeen(exists func(msgID string) bool) {
	for id := range s.Seen {
		if !exists(id) {
			delete(s.Seen, id)
		}
	}
}

// MarkSeen records that sid has rendered msgID.
func (s *State) MarkSeen(msgID, sid string) {
	for _, x := range s.Seen[msgID] {
		if x == sid {
			return
		}
	}
	s.Seen[msgID] = append(s.Seen[msgID], sid)
}

// HasSeen reports whether sid already rendered msgID.
func (s *State) HasSeen(msgID, sid string) bool {
	for _, x := range s.Seen[msgID] {
		if x == sid {
			return true
		}
	}
	return false
}

// AllLiveSeen reports whether every currently-live session has seen msgID —
// the condition to archive a '*' message. With no live session it returns
// false (nothing to deliver to; the message waits).
func (s *State) AllLiveSeen(msgID string, now, grace int64) bool {
	live := 0
	for sid, e := range s.Sessions {
		if now-e.LastSeen > grace {
			continue
		}
		live++
		if !s.HasSeen(msgID, sid) {
			return false
		}
	}
	return live > 0
}

// Forget drops all bookkeeping for a message (called once it is archived).
func (s *State) Forget(msgID string) { delete(s.Seen, msgID) }

func remove(xs []string, v string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
