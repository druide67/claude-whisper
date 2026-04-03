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

@test "sends a message to recipient inbox" {
  run bash "$BIN/whisper-send" test-bob "hello from alice"
  [ "$status" -eq 0 ]
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ ${#files[@]} -eq 1 ]
}

@test "message has correct JSON format" {
  bash "$BIN/whisper-send" test-bob "test message"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  msg="${files[0]}"
  [ "$(jq -r '.from' "$msg")" = "test-alice" ]
  [ "$(jq -r '.to' "$msg")" = "test-bob" ]
  [ "$(jq -r '.content' "$msg")" = "test message" ]
  [ "$(jq -r '.priority' "$msg")" = "normal" ]
  jq -e '.ttl' "$msg"
  jq -e '.timestamp' "$msg"
  jq -e '.id' "$msg"
}

@test "message file has 0600 permissions" {
  bash "$BIN/whisper-send" test-bob "secret"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  perms=$(stat -f "%Lp" "${files[0]}")
  [ "$perms" = "600" ]
}

@test "rejects missing arguments" {
  run bash "$BIN/whisper-send"
  [ "$status" -ne 0 ]
  run bash "$BIN/whisper-send" test-bob
  [ "$status" -ne 0 ]
}

@test "rejects invalid peer-id" {
  run bash "$BIN/whisper-send" "bad peer!" "hello"
  [ "$status" -ne 0 ]
}

@test "no .tmp file left after send" {
  bash "$BIN/whisper-send" test-bob "hello"
  shopt -s nullglob
  tmp_files=("$WHISPER_DIR/inbox/test-bob"/*.tmp)
  [ ${#tmp_files[@]} -eq 0 ]
}

@test "output has visual format" {
  run bash "$BIN/whisper-send" test-bob "hello"
  [[ "$output" == *"📤"* ]]
  [[ "$output" == *"test-bob"* ]]
}
