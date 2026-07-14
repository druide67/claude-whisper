package main

import "testing"

// argv[0] dispatch: a symlink named whisper-<cmd> behaves as `whisper <cmd>`.
func TestArgv0Dispatch(t *testing.T) {
	t.Setenv("WHISPER_DIR", t.TempDir())
	// whisper-list with an empty registry: reaches List (exit 0, "no peers"),
	// proving the name was translated into a subcommand.
	if code := run("/usr/local/bin/whisper-list", nil); code != 0 {
		t.Errorf("whisper-list dispatch failed (exit %d)", code)
	}
	// unknown legacy name → usage error, not a crash
	if code := run("whisper-nope", nil); code != 1 {
		t.Errorf("unknown whisper-* name should exit 1, got %d", code)
	}
	// plain binary name still requires a subcommand
	if code := run("whisper", nil); code != 1 {
		t.Errorf("bare invocation should exit 1, got %d", code)
	}
}
