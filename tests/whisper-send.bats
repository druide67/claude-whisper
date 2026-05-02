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

@test "thread flag adds thread to message" {
  bash "$BIN/whisper-send" -t auth-refactor test-bob "check imports"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ "$(jq -r '.thread' "${files[0]}")" = "auth-refactor" ]
}

@test "thread flag shows in output" {
  run bash "$BIN/whisper-send" -t my-thread test-bob "hello"
  [[ "$output" == *"[my-thread"* ]]
}

@test "--from flag overrides .whisper-peer" {
  run bash "$BIN/whisper-send" -f custom-sender test-bob "hello"
  [ "$status" -eq 0 ]
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ "$(jq -r '.from' "${files[0]}")" = "custom-sender" ]
}

# ---------------------------------------------------------------------------
# G2 — anti-duplicate
# ---------------------------------------------------------------------------

@test "G2: refuses duplicate within window" {
  bash "$BIN/whisper-send" test-bob "same message"
  run bash "$BIN/whisper-send" test-bob "same message"
  [ "$status" -eq 0 ]
  [[ "$output" == *"duplicate"* ]] || [[ "$stderr" == *"duplicate"* ]]
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ ${#files[@]} -eq 1 ]
}

@test "G2: different content is not a duplicate" {
  bash "$BIN/whisper-send" test-bob "first"
  run bash "$BIN/whisper-send" test-bob "second"
  [ "$status" -eq 0 ]
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ ${#files[@]} -eq 2 ]
}

@test "G2: same content to different peer is not a duplicate" {
  bash "$BIN/whisper-send" test-bob "hello"
  run bash "$BIN/whisper-send" test-charlie "hello"
  [ "$status" -eq 0 ]
  shopt -s nullglob
  bob_files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  charlie_files=("$WHISPER_DIR/inbox/test-charlie"/msg-*.json)
  [ ${#bob_files[@]} -eq 1 ]
  [ ${#charlie_files[@]} -eq 1 ]
}

@test "G2: dup window can be overridden via env" {
  WHISPER_DUP_WINDOW=0 bash "$BIN/whisper-send" test-bob "x"
  run env WHISPER_DUP_WINDOW=0 bash "$BIN/whisper-send" test-bob "x"
  [ "$status" -eq 0 ]
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ ${#files[@]} -eq 2 ]
}

# ---------------------------------------------------------------------------
# G3 — hop_count
# ---------------------------------------------------------------------------

@test "G3: first message of a thread has hop_count=0" {
  bash "$BIN/whisper-send" -t loop-test test-bob "hop 0"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ "$(jq -r '.hop_count' "${files[0]}")" = "0" ]
}

@test "G3: message without thread has hop_count=0" {
  bash "$BIN/whisper-send" test-bob "no thread"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ "$(jq -r '.hop_count' "${files[0]}")" = "0" ]
}

@test "G3: hop_count increments when thread already exists in inbox" {
  bash "$BIN/whisper-send" -t chain test-bob "hop 0"
  bash "$BIN/whisper-send" -t chain test-bob "hop 1"
  shopt -s nullglob
  hops=()
  for f in "$WHISPER_DIR/inbox/test-bob"/msg-*.json; do
    hops+=($(jq -r '.hop_count' "$f"))
  done
  # Sort hops to make assertion deterministic regardless of filename order
  IFS=$'\n' sorted=($(sort <<< "${hops[*]}")); unset IFS
  [ "${sorted[0]}" = "0" ]
  [ "${sorted[1]}" = "1" ]
}

@test "G3: output mentions hop=N for threaded messages" {
  bash "$BIN/whisper-send" -t mythread test-bob "first"
  run bash "$BIN/whisper-send" -t mythread test-bob "second"
  [[ "$output" == *"hop=1"* ]]
}
