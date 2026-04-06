#!/usr/bin/env bats

setup() {
  export WHISPER_DIR="$(mktemp -d)"
  export PROJECT_DIR="$(mktemp -d)"
  BIN="$BATS_TEST_DIRNAME/../bin"
}

teardown() {
  rm -rf "$WHISPER_DIR" "$PROJECT_DIR"
}

@test "creates directory structure" {
  run bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  [ "$status" -eq 0 ]
  [ -d "$WHISPER_DIR/inbox/test-alice" ]
  [ -d "$WHISPER_DIR/archive" ]
}

@test "sets correct permissions on whisper dir" {
  bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  perms=$(stat -f "%Lp" "$WHISPER_DIR")
  [ "$perms" = "700" ]
}

@test "writes .whisper-peer in project dir" {
  bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  [ "$(cat "$PROJECT_DIR/.whisper-peer")" = "test-alice" ]
}

@test "creates valid peers.json with cwd" {
  bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  jq -e '.peers["test-alice"].registered' "$WHISPER_DIR/peers.json"
  jq -e '.peers["test-alice"].cwd' "$WHISPER_DIR/peers.json"
}

@test "rejects invalid peer-id" {
  run bash "$BIN/whisper-init" "bad peer!"
  [ "$status" -ne 0 ]
}

@test "rejects empty arguments" {
  run bash "$BIN/whisper-init"
  [ "$status" -ne 0 ]
}

@test "copies hook to whisper dir" {
  bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  [ -x "$WHISPER_DIR/hooks/check-inbox.sh" ]
}

@test "reports hook status" {
  run bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  [[ "$output" == *"Hook"* ]]
}
