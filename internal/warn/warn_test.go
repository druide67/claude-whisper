package warn

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/druide67/claude-whisper/internal/store"
)

func TestWriteThenClear(t *testing.T) {
	p := store.Paths{Root: t.TempDir()}
	if err := Write(p, "delivery-retry", "msg-1-ab", map[string]any{"peer": "bob"}); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(p.Warnings(), "delivery-retry-msg-1-ab.warn")
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("sentinel not written: %v", err)
	}
	fi, _ := os.Stat(f)
	if fi.Mode().Perm() != store.FilePerm {
		t.Errorf("perm = %o, want %o", fi.Mode().Perm(), store.FilePerm)
	}
	Clear(p, "delivery-retry", "msg-1-ab")
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Error("sentinel not cleared")
	}
	// Clear on absent = no panic/error
	Clear(p, "delivery-retry", "does-not-exist")
}
