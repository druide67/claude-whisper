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
