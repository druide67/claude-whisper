package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/druide67/claude-whisper/internal/store"
	"github.com/druide67/claude-whisper/internal/warn"
)

// Anti-loop circuit breaker (state/pair-ledger.json).
//
// hop_count only follows EXPLICIT reply chains (-r): two agents answering each
// other with plain sends never increment it. This breaker counts sends per
// UNORDERED pair {from,to} in a sliding window and trips independently of any
// reply chain:
//   - count ≥ WHISPER_PAIR_SOFT (default 8):  send proceeds, loud stderr warning
//   - count ≥ WHISPER_PAIR_HARD (default 20): send refused (exit 1) + a
//     pair-flood-<a>-<b> sentinel, auto-cleared once a later send on the pair
//     finds the window back under the soft threshold
//
// The ledger is additive state: recent-sends.json (the dedup ledger, a
// documented public file) is left untouched.

type pairLedger struct {
	Entries []pairEntry `json:"entries"`
}
type pairEntry struct {
	TS   int64  `json:"ts"`
	Pair string `json:"pair"`
}

// pairKey returns the canonical unordered form of {a,b}.
func pairKey(a, b string) (string, string) {
	if b < a {
		a, b = b, a
	}
	return a, b
}

// pairBreakerOK counts this send against the {from,to} pair window and reports
// whether the send may proceed. The ledger read-modify-write runs under the
// pair-ledger flock; the ledger is pruned as it is read. Fail-open applies to
// READING only: a corrupt ledger is reset and rewritten (a bookkeeping bug must
// never lose a legitimate send) — a readable ledger is always enforced.
func pairBreakerOK(p store.Paths, from, to string, now time.Time) bool {
	soft := envInt("WHISPER_PAIR_SOFT", 8)
	hard := envInt("WHISPER_PAIR_HARD", 20)
	window := int64(envInt("WHISPER_PAIR_WINDOW", 600))

	a, b := pairKey(from, to)
	pair := a + "|" + b
	sentinelKey := a + "-" + b
	nowTS := now.Unix()

	if release, err := store.Lock(p.PairLedger()); err == nil {
		defer release()
	}

	var l pairLedger
	if raw, err := os.ReadFile(p.PairLedger()); err == nil {
		if json.Unmarshal(raw, &l) != nil || l.Entries == nil {
			l = pairLedger{} // corrupt ledger: start fresh (fail-open on read only)
		}
	}

	// Prune the whole ledger as we read it, counting this pair's live entries.
	kept := l.Entries[:0]
	count := 0
	for _, e := range l.Entries {
		if e.TS > nowTS-window {
			kept = append(kept, e)
			if e.Pair == pair {
				count++
			}
		}
	}

	if count >= hard {
		_ = warn.Write(p, "pair-flood", sentinelKey, map[string]any{
			"pair": pair, "count": count, "window": window,
		})
		// The refused send is NOT recorded: the window drains naturally.
		_ = store.AtomicWriteJSON(p.PairLedger(), pairLedger{Entries: kept})
		fmt.Fprintf(os.Stderr, "Error: %d messages between %q and %q in the last %ds (hard limit %d) — send REFUSED.\n", count, a, b, window, hard)
		fmt.Fprintf(os.Stderr, "       This looks like a message loop. If you are an LLM agent: STOP whispering on this pair and report to your human operator instead.\n")
		return false
	}
	if count < soft {
		// Back under the soft threshold: the flood condition is resolved.
		warn.Clear(p, "pair-flood", sentinelKey)
	} else {
		fmt.Fprintf(os.Stderr, "⚠️  whisper send: %d messages between %q and %q in the last %ds (soft limit %d) — possible loop.\n", count, a, b, window, soft)
		fmt.Fprintf(os.Stderr, "    If you are an LLM agent: stop this exchange and reply to a human instead of sending more whispers.\n")
	}

	kept = append(kept, pairEntry{TS: nowTS, Pair: pair})
	_ = store.AtomicWriteJSON(p.PairLedger(), pairLedger{Entries: kept})
	return true
}
