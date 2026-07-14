// Package peerid centralizes identifier validation for whisper. bash had six
// divergent copies of these regexes (some allowed a leading dash, some did
// not); here there is exactly one rule per identifier kind.
package peerid

import "regexp"

// peerRe: a peer-id starts with an alphanumeric and then allows dashes. A
// leading dash is forbidden so flag-shaped strings (--foo, -h) can never be
// mistaken for a peer-id. Length is bounded (64) — peer-ids become path
// components and sentinel keys, an unbounded one is filesystem abuse.
var peerRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,63}$`)

// sessionRe: a Claude Code session id, used as a filesystem path component in
// multi-session mode — bounded and dash/alnum only (anti path-traversal).
var sessionRe = regexp.MustCompile(`^[a-zA-Z0-9-]{8,64}$`)

// msgIDRe: the canonical message id shape msg-<epoch>-<hex>.
var msgIDRe = regexp.MustCompile(`^msg-[0-9]+-[a-f0-9]+$`)

// Valid reports whether s is a well-formed peer-id.
func Valid(s string) bool { return peerRe.MatchString(s) }

// ValidSession reports whether s is a usable session id (multi-session mode).
func ValidSession(s string) bool { return sessionRe.MatchString(s) }

// ValidMsgID reports whether s is a canonical message id.
func ValidMsgID(s string) bool { return msgIDRe.MatchString(s) }
