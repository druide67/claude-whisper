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
# G3 — hop_count via in_reply_to chain (bug PRISM-2 fix, 2026-05-13)
# ---------------------------------------------------------------------------

@test "G3: message without thread or reply-to has hop_count=0" {
  bash "$BIN/whisper-send" test-bob "no thread"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ "$(jq -r '.hop_count' "${files[0]}")" = "0" ]
}

@test "G3: first message of a thread (no reply-to) has hop_count=0" {
  bash "$BIN/whisper-send" -t mythread test-bob "first"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ "$(jq -r '.hop_count' "${files[0]}")" = "0" ]
}

@test "G3: same thread without reply-to keeps hop=0 (regression PRISM-2 fan-in)" {
  # Simulate 8 independent replies to the same broadcast — none should
  # accumulate hop_count just because they share a thread.
  bash "$BIN/whisper-send" -t broadcast test-bob "reply 1"
  bash "$BIN/whisper-send" -t broadcast test-bob "reply 2"
  bash "$BIN/whisper-send" -t broadcast test-bob "reply 3"
  shopt -s nullglob
  for f in "$WHISPER_DIR/inbox/test-bob"/msg-*.json; do
    [ "$(jq -r '.hop_count' "$f")" = "0" ]
  done
}

@test "G3: --reply-to to a known msg increments hop_count by 1" {
  # Plant a reference message in archive with hop_count=3
  mkdir -p "$WHISPER_DIR/archive"
  REF_ID="msg-1000000000-abcdef01"
  cat > "$WHISPER_DIR/archive/${REF_ID}.json" <<EOF
{"id":"$REF_ID","from":"test-bob","to":"test-alice","content":"prev","hop_count":3,"timestamp":"2026-05-14T00:00:00Z","priority":"normal","ttl":3600}
EOF
  bash "$BIN/whisper-send" -r "$REF_ID" test-bob "reply"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ "$(jq -r '.hop_count' "${files[0]}")" = "4" ]
  [ "$(jq -r '.in_reply_to' "${files[0]}")" = "$REF_ID" ]
}

@test "G3: --reply-to to unknown msg-id sends hop=0 with stderr warning" {
  run bash "$BIN/whisper-send" -r "msg-1000000000-deadbeef" test-bob "reply"
  [ "$status" -eq 0 ]
  [[ "$output" == *"not found"* ]] || [[ "$stderr" == *"not found"* ]]
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ "$(jq -r '.hop_count' "${files[0]}")" = "0" ]
}

@test "G3: --reply-to with malformed msg-id is rejected" {
  run bash "$BIN/whisper-send" -r "not-a-valid-id" test-bob "reply"
  [ "$status" -ne 0 ]
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ ${#files[@]} -eq 0 ]
}

@test "G3: explicit reply chain (A→B→A→B) produces hop 0,1,2,3" {
  mkdir -p "$WHISPER_DIR/archive"
  # Msg 1: hop=0 (no reply-to)
  bash "$BIN/whisper-send" test-bob "msg1"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  M1_ID=$(jq -r '.id' "${files[0]}")
  # Move to archive (simulate it's been seen) for cleaner test
  mv "${files[0]}" "$WHISPER_DIR/archive/"
  # Msg 2: reply to msg 1 → hop=1
  bash "$BIN/whisper-send" -r "$M1_ID" test-bob "msg2"
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  M2_ID=$(jq -r '.id' "${files[0]}")
  [ "$(jq -r '.hop_count' "${files[0]}")" = "1" ]
  mv "${files[0]}" "$WHISPER_DIR/archive/"
  # Msg 3: reply to msg 2 → hop=2
  bash "$BIN/whisper-send" -r "$M2_ID" test-bob "msg3"
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  [ "$(jq -r '.hop_count' "${files[0]}")" = "2" ]
}

@test "G3: in_reply_to field absent when -r not provided" {
  bash "$BIN/whisper-send" test-bob "no reply"
  shopt -s nullglob
  files=("$WHISPER_DIR/inbox/test-bob"/msg-*.json)
  # jq -r '.in_reply_to' on absent field returns "null" string
  [ "$(jq -r '.in_reply_to // empty' "${files[0]}")" = "" ]
}
