#!/usr/bin/env bats

setup() {
  export WHISPER_DIR="$(mktemp -d)"
  export PROJECT_DIR="$(mktemp -d)"
  BIN="$BATS_TEST_DIRNAME/../bin"
  bash "$BIN/whisper-init" test-alice "$PROJECT_DIR" > /dev/null
  cd "$PROJECT_DIR"
}

teardown() {
  rm -rf "$WHISPER_DIR" "$PROJECT_DIR"
}

@test "doctor: clean install reports no critical issues" {
  run bash "$BIN/whisper-doctor"
  [ "$status" -eq 0 ]
  [[ "$output" == *"whisper-doctor"* ]]
  [[ "$output" == *"summary"* ]]
}

@test "doctor: detects missing .whisper-peer at registered cwd" {
  rm -f "$PROJECT_DIR/.whisper-peer"
  run bash "$BIN/whisper-doctor"
  [[ "$output" == *"MISSING"* ]]
  [[ "$output" == *"test-alice"* ]]
}

@test "doctor: --fix --yes recreates missing .whisper-peer" {
  rm -f "$PROJECT_DIR/.whisper-peer"
  run bash "$BIN/whisper-doctor" --fix --yes
  [ -f "$PROJECT_DIR/.whisper-peer" ]
  [ "$(cat "$PROJECT_DIR/.whisper-peer")" = "test-alice" ]
}

@test "doctor: detects whisper-peer content mismatch" {
  echo "wrong-name" > "$PROJECT_DIR/.whisper-peer"
  run bash "$BIN/whisper-doctor"
  [[ "$output" == *"mismatch"* ]]
}

@test "doctor: --fix --yes corrects loose dir perms" {
  chmod 755 "$WHISPER_DIR/inbox/test-alice"
  run bash "$BIN/whisper-doctor" --fix --yes
  perms=$(stat -f "%Lp" "$WHISPER_DIR/inbox/test-alice")
  [ "$perms" = "700" ]
}

@test "doctor: --fix --yes corrects loose message file perms" {
  mkdir -p "$WHISPER_DIR/inbox/test-alice"
  echo '{"id":"x"}' > "$WHISPER_DIR/inbox/test-alice/msg-x.json"
  chmod 644 "$WHISPER_DIR/inbox/test-alice/msg-x.json"
  run bash "$BIN/whisper-doctor" --fix --yes
  perms=$(stat -f "%Lp" "$WHISPER_DIR/inbox/test-alice/msg-x.json")
  [ "$perms" = "600" ]
}

@test "doctor: reports stuck messages in run/processing/ (informational, not fixed)" {
  mkdir -p "$WHISPER_DIR/run/processing"
  echo '{"id":"stuck"}' > "$WHISPER_DIR/run/processing/msg-stuck.json"
  # Force mtime > 1h ago
  touch -t 202504010000 "$WHISPER_DIR/run/processing/msg-stuck.json"
  run bash "$BIN/whisper-doctor"
  [[ "$output" == *"stuck"* ]]
  # File not moved (informational only)
  [ -f "$WHISPER_DIR/run/processing/msg-stuck.json" ]
}

@test "doctor: surfaces state/warnings/ sentinels" {
  mkdir -p "$WHISPER_DIR/state/warnings"
  printf '{"ts":"x","cwd":"/tmp","kind":"missing-peer","suggested_peer":"foo"}\n' \
    > "$WHISPER_DIR/state/warnings/missing-peer-abc123.warn"
  run bash "$BIN/whisper-doctor"
  [[ "$output" == *"sentinel"* ]]
  [[ "$output" == *"missing-peer-abc123"* ]]
}

@test "doctor: flags stale peer (last_seen > 7d)" {
  jq '.peers."test-alice".last_seen = "2026-01-01T00:00:00Z"' \
    "$WHISPER_DIR/peers.json" > "$WHISPER_DIR/peers.json.tmp" && \
    mv "$WHISPER_DIR/peers.json.tmp" "$WHISPER_DIR/peers.json"
  run bash "$BIN/whisper-doctor"
  [[ "$output" == *"last_seen"* ]]
  [[ "$output" == *"test-alice"* ]]
}

@test "doctor: --help shows usage" {
  run bash "$BIN/whisper-doctor" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage:"* ]]
  [[ "$output" == *"Checks:"* ]]
}

@test "doctor: exits non-zero when critical and not fixed" {
  rm -f "$PROJECT_DIR/.whisper-peer"
  run bash "$BIN/whisper-doctor"
  [ "$status" -ne 0 ]
}

@test "doctor: exits zero when critical is fixed via --fix --yes" {
  rm -f "$PROJECT_DIR/.whisper-peer"
  run bash "$BIN/whisper-doctor" --fix --yes
  [ "$status" -eq 0 ]
}
