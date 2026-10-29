package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShellValuesAreLiteral(t *testing.T) {
	for _, value := range []string{"plain", "path with spaces", "it's a path", "$HOME; $(touch /unwanted)", "back\\slash"} {
		decoded, err := shellValue(quoteValue(value))
		if err != nil || decoded != value {
			t.Fatalf("%q => %q (%v)", value, decoded, err)
		}
	}
	decoded, err := shellValue(`/a\ path`)
	if err != nil || decoded != "/a path" {
		t.Fatal(decoded, err)
	}
	for _, unsafe := range []string{"$(touch bad)", "`touch bad`", "value; echo bad", "'unclosed"} {
		if _, err := shellValue(unsafe); err == nil {
			t.Fatalf("accepted %s", unsafe)
		}
	}
}

func TestCopyDoesNotOverwriteOrEscape(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, ".env"), "secret")
	write(filepath.Join(dst, ".env"), "tracked")
	if err := copyEntry(filepath.Join(src, ".env"), filepath.Join(dst, ".env"), src, dst); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dst, ".env"))
	if string(data) != "tracked" {
		t.Fatal("overwrote destination")
	}
	if err := os.Symlink(outside, filepath.Join(dst, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := copyEntry(filepath.Join(src, ".env"), filepath.Join(dst, "escape", ".env"), src, dst); err == nil {
		t.Fatal("symlink traversal accepted")
	}
	for _, pattern := range []string{"../secret", ".git/config", "foo/../secret", ".worktree.env"} {
		if !unsafeRelative(pattern) {
			t.Fatalf("unsafe path %q", pattern)
		}
	}
}
