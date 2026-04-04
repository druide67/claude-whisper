#!/usr/bin/env bats

setup() {
  export WHISPER_DIR="$(mktemp -d)"
  export PROJECT_DIR="$(mktemp -d)"
  BIN="$BATS_TEST_DIRNAME/../bin"
  bash "$BIN/whisper-init" test-alice "$PROJECT_DIR"
  cd "$PROJECT_DIR"
}

teardown() {
  rm -rf "$WHISPER_DIR" "$PROJECT_DIR"
}

@test "lists registered peers" {
  run bash "$BIN/whisper-list"
  [ "$status" -eq 0 ]
  [[ "$output" == *"test-alice"* ]]
  [[ "$output" == *"(you)"* ]]
}

@test "shows inbox count 0 for empty inbox" {
  run bash "$BIN/whisper-list"
  [[ "$output" == *"inbox: 0"* ]]
}

@test "shows correct inbox count with messages" {
  mkdir -p "$WHISPER_DIR/inbox/test-alice"
  echo '{}' > "$WHISPER_DIR/inbox/test-alice/msg-test-1.json"
  echo '{}' > "$WHISPER_DIR/inbox/test-alice/msg-test-2.json"
  run bash "$BIN/whisper-list"
  [[ "$output" == *"inbox: 2"* ]]
}

@test "fails when no peers registered" {
  rm "$WHISPER_DIR/peers.json"
  run bash "$BIN/whisper-list"
  [ "$status" -ne 0 ]
}
