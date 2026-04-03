#!/usr/bin/env bats

setup() {
  export WHISPER_DIR="$(mktemp -d)"
  export PROJECT_DIR="$(mktemp -d)"
  BIN="$BATS_TEST_DIRNAME/../bin"
  # Init sender
  bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  # Register other peers
  jq '.peers.bob = {registered: "2026-01-01T00:00:00Z", last_seen: "2026-01-01T00:00:00Z"} | .peers.carol = {registered: "2026-01-01T00:00:00Z", last_seen: "2026-01-01T00:00:00Z"}' \
    "$WHISPER_DIR/peers.json" > "$WHISPER_DIR/peers.json.tmp" && mv "$WHISPER_DIR/peers.json.tmp" "$WHISPER_DIR/peers.json"
  cd "$PROJECT_DIR"
}

teardown() {
  rm -rf "$WHISPER_DIR" "$PROJECT_DIR"
}

@test "broadcasts to all peers except self" {
  run bash "$BIN/whisper-broadcast" "hello everyone"
  [ "$status" -eq 0 ]
  [[ "$output" == *"📢"* ]]
  [[ "$output" == *"2 peer(s)"* ]]
  # Check messages in inboxes
  shopt -s nullglob
  bob_msgs=("$WHISPER_DIR/inbox/bob"/msg-*.json)
  carol_msgs=("$WHISPER_DIR/inbox/carol"/msg-*.json)
  [ ${#bob_msgs[@]} -eq 1 ]
  [ ${#carol_msgs[@]} -eq 1 ]
}

@test "does not send to self" {
  bash "$BIN/whisper-broadcast" "test"
  shopt -s nullglob
  self_msgs=("$WHISPER_DIR/inbox/test-alice"/msg-*.json)
  [ ${#self_msgs[@]} -eq 0 ]
}

@test "rejects missing arguments" {
  run bash "$BIN/whisper-broadcast"
  [ "$status" -ne 0 ]
}
