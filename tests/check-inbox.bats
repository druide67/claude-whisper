#!/usr/bin/env bats

setup() {
  export WHISPER_DIR="$(mktemp -d)"
  export PROJECT_DIR="$(mktemp -d)"
  BIN="$BATS_TEST_DIRNAME/../bin"
  HOOKS="$BATS_TEST_DIRNAME/../hooks"
  bash "$BIN/whisper-init" test-bob "$PROJECT_DIR"
  cd "$PROJECT_DIR"
}

teardown() {
  rm -rf "$WHISPER_DIR" "$PROJECT_DIR"
}

@test "exits silently when inbox is empty" {
  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "returns messages with visual format" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-test-1", from: "test-alice", to: "test-bob", timestamp: $ts, content: "hello bob", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-test-1.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"whisper"* ]]
  [[ "$output" == *"test-alice"* ]]
  [[ "$output" == *"hello bob"* ]]
}

@test "archives messages after reading" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-test-2", from: "test-alice", to: "test-bob", timestamp: $ts, content: "archive me", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-test-2.json"

  bash "$HOOKS/check-inbox.sh" > /dev/null
  [ ! -f "$WHISPER_DIR/inbox/test-bob/msg-test-2.json" ]
  [ -f "$WHISPER_DIR/archive/msg-test-2.json" ]
}

@test "exits silently when no peer configured" {
  rm -f "$PROJECT_DIR/.whisper-peer" "$WHISPER_DIR/.current-peer"
  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "writes last sender to state file for whisper-reply" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-test-3", from: "test-alice", to: "test-bob", timestamp: $ts, content: "ping", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-test-3.json"

  bash "$HOOKS/check-inbox.sh" > /dev/null
  [ -f "$WHISPER_DIR/state/last-sender-test-bob" ]
  [ "$(cat "$WHISPER_DIR/state/last-sender-test-bob")" = "test-alice" ]
}

@test "last sender is the last message processed when multiple arrive" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-001", from: "test-alice", to: "test-bob", timestamp: $ts, content: "first", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-001.json"
  jq -n --arg ts "$NOW" \
    '{id: "msg-002", from: "test-carol", to: "test-bob", timestamp: $ts, content: "second", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-002.json"

  bash "$HOOKS/check-inbox.sh" > /dev/null
  [ "$(cat "$WHISPER_DIR/state/last-sender-test-bob")" = "test-carol" ]
}

# --- v0.3.1: inline-or-reference + archive-after-display --------------------

# Helper: generate a message with a content of exactly N chars
_make_msg() {
  local path="$1" from="$2" len="$3" thread="${4:-}"
  local content
  content=$(printf 'x%.0s' $(seq 1 "$len"))
  local NOW
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg from "$from" --arg ts "$NOW" --arg content "$content" --arg thread "$thread" \
    '{id: "msg-test", from: $from, to: "test-bob", timestamp: $ts, content: $content, priority: "normal", ttl: 3600}
     | if $thread != "" then .thread = $thread else . end' \
    > "$path"
}

@test "big message becomes a reference with archive path" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  _make_msg "$WHISPER_DIR/inbox/test-bob/msg-big-1.json" "test-alice" 5000

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"📂"* ]]
  [[ "$output" == *"5000 chars"* ]]
  [[ "$output" == *"Read $WHISPER_DIR/archive/msg-big-1.json"* ]]
  [[ "$output" == *"Preview:"* ]]
}

@test "big message preview is bounded to ~300 chars" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  _make_msg "$WHISPER_DIR/inbox/test-bob/msg-big-2.json" "test-alice" 10000

  run bash "$HOOKS/check-inbox.sh"
  # Extract the "Preview: xxxx…" line and count the x's
  preview_line=$(echo "$output" | grep '^> Preview:' | head -1)
  # Count trailing x's after "Preview: "
  [[ -n "$preview_line" ]]
  # The preview should be at most 300 chars of content (plus the "Preview: " prefix and trailing "…")
  preview_content=${preview_line#> Preview: }
  preview_content=${preview_content%…}
  [ ${#preview_content} -le 300 ]
}

@test "WHISPER_INLINE_MAX env var controls the inline threshold" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  _make_msg "$WHISPER_DIR/inbox/test-bob/msg-inline-env.json" "test-alice" 600

  # With low threshold → should become reference
  run env WHISPER_INLINE_MAX=500 bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"📂"* ]]
  [[ "$output" == *"600 chars"* ]]
}

@test "overflow: messages beyond budget stay in inbox" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  # 20 messages of 500 chars content each — with markdown wrapping, cumulative
  # inline CONTEXT vastly exceeds the default OUTPUT_MAX of 8000.
  for i in $(seq -f '%03g' 1 20); do
    _make_msg "$WHISPER_DIR/inbox/test-bob/msg-$i.json" "peer-$i" 500
  done

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"affiché(s)"* ]]
  [[ "$output" == *"en attente"* ]]
  [[ "$output" == *"prochain prompt"* ]]

  # Inbox should still contain at least some un-processed messages
  shopt -s nullglob
  remaining=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ ${#remaining[@]} -gt 0 ]
}

@test "archive contains only displayed messages (no speculative archival)" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  for i in $(seq -f '%03g' 1 20); do
    _make_msg "$WHISPER_DIR/inbox/test-bob/msg-$i.json" "peer-$i" 500
  done

  bash "$HOOKS/check-inbox.sh" > /tmp/whisper-hook-out.$$.txt
  # Count displayed messages from the header "━━━ 📨 whisper — N affiché(s) / M en attente ━━━"
  displayed=$(grep -oE '[0-9]+ affiché' /tmp/whisper-hook-out.$$.txt | head -1 | grep -oE '[0-9]+')
  rm -f /tmp/whisper-hook-out.$$.txt
  [ -n "$displayed" ]
  shopt -s nullglob
  archived=("$WHISPER_DIR/archive"/msg-*.json)
  [ ${#archived[@]} -eq "$displayed" ]
}

@test "single huge message is always displayed (even if it alone exceeds OUTPUT_MAX)" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  _make_msg "$WHISPER_DIR/inbox/test-bob/msg-huge.json" "test-alice" 50000

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  # Reference format, with archive path
  [[ "$output" == *"📂"* ]]
  [[ "$output" == *"50000 chars"* ]]
  # Message must have been archived
  [ -f "$WHISPER_DIR/archive/msg-huge.json" ]
}

@test "small message remains inlined exactly like before (regression)" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-small", from: "test-alice", to: "test-bob", timestamp: $ts, content: "hello world", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-small.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"hello world"* ]]
  [[ "$output" != *"📂"* ]]
  [[ "$output" != *"Preview:"* ]]
}

# ---------------------------------------------------------------------------
# G3 — hop_count drop
# ---------------------------------------------------------------------------

@test "G3: renders messages with hop_count > HOP_MAX with a visible warning" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-hop-9", from: "test-alice", to: "test-bob", timestamp: $ts, content: "loop suspect", priority: "normal", ttl: 3600, hop_count: 9}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-hop-9.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"loop suspect"* ]]                        # rendered, not silently dropped
  [[ "$output" == *"HOP=9"* ]]                                # warning is visible
  [ ! -f "$WHISPER_DIR/inbox/test-bob/msg-hop-9.json" ]      # archived after render
  [ -f "$WHISPER_DIR/archive/msg-hop-9.json" ]
}

@test "G3: delivers messages with hop_count = HOP_MAX without warning (boundary)" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-hop-8", from: "test-alice", to: "test-bob", timestamp: $ts, content: "still delivered", priority: "normal", ttl: 3600, hop_count: 8}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-hop-8.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"still delivered"* ]]
  [[ "$output" != *"HOP=8"* ]]                                # no warning at the boundary
}

@test "G3: silent drop above HOP_HARD (runaway loop)" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-hop-25", from: "test-alice", to: "test-bob", timestamp: $ts, content: "runaway", priority: "normal", ttl: 3600, hop_count: 25}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-hop-25.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -f "$WHISPER_DIR/archive/msg-hop-25.json" ]
}

@test "G3: WHISPER_HOP_MAX env var changes the soft threshold" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-hop-3", from: "test-alice", to: "test-bob", timestamp: $ts, content: "still delivered", priority: "normal", ttl: 3600, hop_count: 3}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-hop-3.json"

  WHISPER_HOP_MAX=2 run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"still delivered"* ]]
  [[ "$output" == *"HOP=3"* ]]
}

@test "G3: includes PRIORITY instruction in output" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-prio", from: "test-alice", to: "test-bob", timestamp: $ts, content: "needs PRIORITY line", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-prio.json"

  run bash "$HOOKS/check-inbox.sh"
  [[ "$output" == *"PRIORITY"* ]]
}

# ---------------------------------------------------------------------------
# Fallback scan of run/processing/ (resilience when v1.0 leader is down)
# ---------------------------------------------------------------------------

@test "processing/ fallback: delivers stuck messages addressed to our peer" {
  mkdir -p "$WHISPER_DIR/run/processing"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-stuck-1", from: "test-alice", to: "test-bob", timestamp: $ts, content: "rescued from processing", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/run/processing/msg-stuck-1.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"rescued from processing"* ]]
  [ ! -f "$WHISPER_DIR/run/processing/msg-stuck-1.json" ]
  [ -f "$WHISPER_DIR/archive/msg-stuck-1.json" ]
}

@test "processing/ fallback: ignores messages addressed to other peers" {
  mkdir -p "$WHISPER_DIR/run/processing"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-other", from: "test-alice", to: "someone-else", timestamp: $ts, content: "not for us", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/run/processing/msg-other.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -f "$WHISPER_DIR/run/processing/msg-other.json" ]   # untouched
}

# ---------------------------------------------------------------------------
# R2 — Observability sentinels (fail-loud) + R3 last_seen heartbeat
# ---------------------------------------------------------------------------

@test "R2: missing .whisper-peer writes a sentinel when inbox has pending msgs" {
  # Plant a message in a peer-dir matching the basename of CWD
  PEER_NAME=$(basename "$PROJECT_DIR")
  mkdir -p "$WHISPER_DIR/inbox/$PEER_NAME"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" --arg to "$PEER_NAME" \
    '{id: "msg-stranded", from: "x", to: $to, timestamp: $ts, content: "x", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/$PEER_NAME/msg-stranded.json"
  # Remove the .whisper-peer file
  rm -f "$PROJECT_DIR/.whisper-peer"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [ -z "$output" ]                                                    # still silent stdout
  shopt -s nullglob
  warns=("$WHISPER_DIR/state/warnings"/missing-peer-*.warn)
  [ ${#warns[@]} -ge 1 ]                                              # sentinel created
  [[ "$(cat "${warns[0]}")" == *"$PEER_NAME"* ]]                      # mentions suggested peer
}

@test "R2: missing .whisper-peer with no candidate inbox writes no sentinel" {
  # Empty inbox dir → no plausible suggestion → no warning to avoid noise
  rm -f "$PROJECT_DIR/.whisper-peer"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  shopt -s nullglob
  warns=("$WHISPER_DIR/state/warnings"/missing-peer-*.warn)
  [ ${#warns[@]} -eq 0 ]
}

@test "R2: invalid peer-id content writes an invalid-peer sentinel" {
  echo "bad peer!" > "$PROJECT_DIR/.whisper-peer"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  shopt -s nullglob
  warns=("$WHISPER_DIR/state/warnings"/invalid-peer-*.warn)
  [ ${#warns[@]} -ge 1 ]
}

@test "R2: successful run clears prior missing-peer sentinel for the same CWD" {
  # First run without .whisper-peer + plausible inbox → write sentinel
  PEER_NAME=$(basename "$PROJECT_DIR")
  mkdir -p "$WHISPER_DIR/inbox/$PEER_NAME"
  jq -n '{id: "x", from: "y", to: "z", timestamp: "x", content: "x", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/$PEER_NAME/msg-tmp.json"
  rm -f "$PROJECT_DIR/.whisper-peer"
  bash "$HOOKS/check-inbox.sh" > /dev/null
  shopt -s nullglob
  warns=("$WHISPER_DIR/state/warnings"/missing-peer-*.warn)
  [ ${#warns[@]} -ge 1 ]

  # Second run with .whisper-peer restored → sentinel cleared
  echo "test-bob" > "$PROJECT_DIR/.whisper-peer"
  rm -f "$WHISPER_DIR/inbox/$PEER_NAME/msg-tmp.json"
  bash "$HOOKS/check-inbox.sh" > /dev/null
  warns=("$WHISPER_DIR/state/warnings"/missing-peer-*.warn)
  [ ${#warns[@]} -eq 0 ]
}

@test "R2: hop-overflow drop writes a per-message sentinel" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-hop-50", from: "test-alice", to: "test-bob", timestamp: $ts, content: "runaway", priority: "normal", ttl: 3600, hop_count: 50, thread: "loopy"}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-hop-50.json"

  bash "$HOOKS/check-inbox.sh" > /dev/null
  shopt -s nullglob
  warns=("$WHISPER_DIR/state/warnings"/hop-overflow-msg-hop-50.warn)
  [ ${#warns[@]} -eq 1 ]
  [[ "$(cat "${warns[0]}")" == *"msg-hop-50"* ]]
}

@test "R3: hook updates peers.json[PEER_ID].last_seen on successful run" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-hb", from: "alice", to: "test-bob", timestamp: $ts, content: "heartbeat", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-hb.json"
  # Sentinel: nuke the existing last_seen so we can detect the update
  jq '.peers."test-bob".last_seen = "2026-01-01T00:00:00Z"' \
    "$WHISPER_DIR/peers.json" > "$WHISPER_DIR/peers.json.tmp" && mv "$WHISPER_DIR/peers.json.tmp" "$WHISPER_DIR/peers.json"

  bash "$HOOKS/check-inbox.sh" > /dev/null
  LAST=$(jq -r '.peers."test-bob".last_seen' "$WHISPER_DIR/peers.json")
  # Should be a 2026 date, not the 2026-01-01 sentinel
  [[ "$LAST" =~ ^2026-[0-9]{2}-[0-9]{2}T ]]
  [ "$LAST" != "2026-01-01T00:00:00Z" ]
}

@test "processing/ fallback: combines with inbox/<peer>/ in same run" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob" "$WHISPER_DIR/run/processing"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-A", from: "alice", to: "test-bob", timestamp: $ts, content: "from inbox", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-A.json"
  jq -n --arg ts "$NOW" \
    '{id: "msg-B", from: "bob", to: "test-bob", timestamp: $ts, content: "from processing", priority: "normal", ttl: 3600}' \
    > "$WHISPER_DIR/run/processing/msg-B.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"from inbox"* ]]
  [[ "$output" == *"from processing"* ]]
  [ -f "$WHISPER_DIR/archive/msg-A.json" ]
  [ -f "$WHISPER_DIR/archive/msg-B.json" ]
}
