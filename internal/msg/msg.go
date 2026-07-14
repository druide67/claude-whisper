// Package msg defines the wire format of a whisper message. The JSON schema
// must stay identical to what the bash tooling wrote (bin/whisper-send) so the
// Swift menu-bar app and the VS Code extension keep parsing it unchanged.
package msg

import (
	"encoding/json"
	"fmt"
	"time"
)

// Message is one inbox item: inbox/<to>/msg-<epoch>-<hex>.json.
// Field order matches the historical jq output; thread/in_reply_to are omitted
// when empty. Unknown fields on read are ignored (forward-compatible).
type Message struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
	Priority  string `json:"priority"`
	TTL       int    `json:"ttl"`
	HopCount  int    `json:"hop_count"`
	Thread    string `json:"thread,omitempty"`
	InReplyTo string `json:"in_reply_to,omitempty"`
	// Session optionally routes the message to ONE session of the recipient
	// peer, addressed by its user-set title ("*" = every live session).
	// Absent = classic peer-level delivery. Pre-existing consumers ignore it.
	Session string `json:"session,omitempty"`
}

// TimestampFormat is the ISO-8601 UTC layout used for the timestamp field.
const TimestampFormat = "2006-01-02T15:04:05Z"

// NewID builds the canonical id msg-<epoch>-<hex> from an epoch (seconds) and
// raw random bytes (rendered lowercase hex). Kept pure for testability.
func NewID(epoch int64, rnd []byte) string {
	return fmt.Sprintf("msg-%d-%x", epoch, rnd)
}

// FormatTimestamp renders t as the canonical ISO-8601 UTC string.
func FormatTimestamp(t time.Time) string { return t.UTC().Format(TimestampFormat) }

// Marshal serializes a message as compact JSON with a trailing newline. The
// existing consumers are whitespace-agnostic JSON parsers, so compact is fine.
func Marshal(m *Message) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Parse decodes a message file. Missing priority/ttl default to the canonical
// values so hand-written or legacy files stay well-formed. Out-of-range
// numerics are clamped: a negative hop_count (e.g. crafted via the transport
// deposit) would undermine loop detection, never trust it.
func Parse(b []byte) (*Message, error) {
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Priority == "" {
		m.Priority = "normal"
	}
	if m.TTL <= 0 {
		m.TTL = 3600
	}
	if m.HopCount < 0 {
		m.HopCount = 0
	}
	return &m, nil
}
