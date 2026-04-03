#!/usr/bin/env bats

setup() {
  export WHISPER_DIR="$(mktemp -d)"
  BIN="$BATS_TEST_DIRNAME/../bin"
}

teardown() {
  rm -rf "$WHISPER_DIR"
}

@test "creates directory structure" {
  run bash "$BIN/whisper-init" test-alice
  [ "$status" -eq 0 ]
  [ -d "$WHISPER_DIR/inbox/test-alice" ]
  [ -d "$WHISPER_DIR/archive" ]
}

@test "sets correct permissions on whisper dir" {
  bash "$BIN/whisper-init" test-alice
  perms=$(stat -f "%Lp" "$WHISPER_DIR")
  [ "$perms" = "700" ]
}

@test "writes .current-peer" {
  bash "$BIN/whisper-init" test-alice
  [ "$(cat "$WHISPER_DIR/.current-peer")" = "test-alice" ]
}

@test "creates valid peers.json" {
  bash "$BIN/whisper-init" test-alice
  jq -e '.peers["test-alice"].registered' "$WHISPER_DIR/peers.json"
  jq -e '.peers["test-alice"].last_seen' "$WHISPER_DIR/peers.json"
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
  bash "$BIN/whisper-init" test-alice
  [ -x "$WHISPER_DIR/hooks/check-inbox.sh" ]
}

@test "reports hook status" {
  run bash "$BIN/whisper-init" test-alice
  [[ "$output" == *"Hook:"* ]]
}
