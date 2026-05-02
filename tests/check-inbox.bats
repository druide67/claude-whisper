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

@test "G3: drops messages with hop_count > 5" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-hop-6", from: "test-alice", to: "test-bob", timestamp: $ts, content: "should be dropped", priority: "normal", ttl: 3600, hop_count: 6}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-hop-6.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [ -z "$output" ]                                           # silent drop
  [ ! -f "$WHISPER_DIR/inbox/test-bob/msg-hop-6.json" ]      # moved
  [ -f "$WHISPER_DIR/archive/msg-hop-6.json" ]               # archived
}

@test "G3: delivers messages with hop_count = 5 (boundary)" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-hop-5", from: "test-alice", to: "test-bob", timestamp: $ts, content: "still delivered", priority: "normal", ttl: 3600, hop_count: 5}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-hop-5.json"

  run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"still delivered"* ]]
}

@test "G3: WHISPER_HOP_MAX env var changes the threshold" {
  mkdir -p "$WHISPER_DIR/inbox/test-bob"
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq -n --arg ts "$NOW" \
    '{id: "msg-hop-3", from: "test-alice", to: "test-bob", timestamp: $ts, content: "drop me", priority: "normal", ttl: 3600, hop_count: 3}' \
    > "$WHISPER_DIR/inbox/test-bob/msg-hop-3.json"

  WHISPER_HOP_MAX=2 run bash "$HOOKS/check-inbox.sh"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
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
