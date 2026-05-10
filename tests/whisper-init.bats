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

@test "--help shows usage and exits 0 without registering a peer" {
  run bash -c "bash '$BIN/whisper-init' --help 2>&1"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage:"* ]]
  [ ! -f "$WHISPER_DIR/peers.json" ] || ! jq -e '.peers["--help"]' "$WHISPER_DIR/peers.json"
  [ ! -d "$WHISPER_DIR/inbox/--help" ]
}

@test "-h shows usage and exits 0" {
  run bash -c "bash '$BIN/whisper-init' -h 2>&1"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage:"* ]]
}

@test "rejects peer-id starting with a dash (flag-shaped)" {
  run bash "$BIN/whisper-init" --some-flag "$PROJECT_DIR"
  [ "$status" -ne 0 ]
  [ ! -d "$WHISPER_DIR/inbox/--some-flag" ]
}

@test "copies hook to whisper dir" {
  bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  [ -x "$WHISPER_DIR/hooks/check-inbox.sh" ]
}

@test "reports hook status" {
  run bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  [[ "$output" == *"Hook"* ]]
}
