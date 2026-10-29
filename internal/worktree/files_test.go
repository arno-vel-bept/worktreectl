package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/arno-vel-bept/worktreectl/internal/config"
	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
	"github.com/arno-vel-bept/worktreectl/internal/repo"
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

func TestPrintedShellPathsCannotExpandExpressions(t *testing.T) {
	dir := t.TempDir()
	for _, value := range []string{"plain", "path with spaces", "it's a path", "$HOME; $(touch should-not-exist)", "path\nwith newline"} {
		command := exec.Command("/bin/sh", "-c", "printf '%s' "+quoteValue(value))
		command.Dir = dir
		out, err := command.CombinedOutput()
		if err != nil || string(out) != value {
			t.Fatalf("%q became %q (%v)", value, out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("printed shell path executed a command substitution", err)
	}
}

func TestCopyRejectsNonportableVirtualenvs(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	venv := filepath.Join(src, ".venv")
	if err := os.Mkdir(venv, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	err := copyEntry(venv, filepath.Join(dst, ".venv"), src, dst)
	if err == nil || diagnostic.Hint(err) == "" {
		t.Fatal("copied a nonportable environment", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".venv")); !os.IsNotExist(err) {
		t.Fatal("destination was modified", err)
	}
	if err := os.Mkdir(filepath.Join(venv, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "bin", "python"), []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	s := &Service{Repo: &repo.Repo{Main: src}, Config: config.Settings{Copy: []string{".venv/bin/*"}}}
	if err := s.copyIncludes(dst); err == nil || diagnostic.Hint(err) == "" {
		t.Fatal("partial-copy pattern bypassed virtualenv guard", err)
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
