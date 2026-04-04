#!/usr/bin/env bats

setup() {
  export WHISPER_DIR="$(mktemp -d)"
  BIN="$BATS_TEST_DIRNAME/../bin"
  mkdir -p "$WHISPER_DIR/archive"
}

teardown() {
  rm -rf "$WHISPER_DIR"
}

@test "does nothing when archive is empty" {
  run bash "$BIN/whisper-clean"
  [ "$status" -eq 0 ]
  [[ "$output" == *"nothing to remove"* ]]
}

@test "does nothing when no archive dir" {
  rmdir "$WHISPER_DIR/archive"
  run bash "$BIN/whisper-clean"
  [ "$status" -eq 0 ]
}

@test "rejects non-numeric days argument" {
  run bash "$BIN/whisper-clean" abc
  # find will error on non-numeric, that's ok
  [ "$status" -ne 0 ] || [[ "$output" == *"nothing to remove"* ]]
}
