package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mettjs/cairnpost/internal/config"
)

// A send with no file says nothing of files; a file is sent under its
// own name; what is not a file is not read.
func TestWhatTheSendCommandAttaches(t *testing.T) {
	if got, err := attached(nil); got != nil || err != nil {
		t.Fatalf("with no file: %v %v", got, err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := attached(paths{file})
	if err != nil || len(got) != 1 || got[0]["filename"] != "note.txt" || got[0]["content"] != "aGk=" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := attached(paths{dir}); err == nil {
		t.Fatal("a folder was read as a file")
	}
}

// With files off, a send's body is no larger than any other's.
func TestASendsBodyGrowsOnlyForFiles(t *testing.T) {
	cfg := config.Config{MaxBodyBytes: 1000, MaxAttachments: 0, MaxAttachmentBytes: 3000}
	if emailBytes(cfg) != 1000 || attachmentsOf(cfg) >= 0 {
		t.Fatalf("with files off: %d %d", emailBytes(cfg), attachmentsOf(cfg))
	}
	cfg.MaxAttachments = 2
	if got := emailBytes(cfg); got != 1000+4000+2048 || attachmentsOf(cfg) != 2 {
		t.Fatalf("with files on: %d", got)
	}
}
