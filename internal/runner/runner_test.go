package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestChildExitCode(t *testing.T) {
	err := Run(context.Background(), "", nil, nil, io.Discard, io.Discard, "/bin/sh", "-c", "exit 17")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 17 {
		t.Fatalf("exit error: %v", err)
	}
}

func TestRunResolvesWorktreePathInsteadOfParentPath(t *testing.T) {
	parent, local := t.TempDir(), t.TempDir()
	for path, text := range map[string]string{parent: "parent", local: "local"} {
		if err := os.WriteFile(filepath.Join(path, "python"), []byte("#!/bin/sh\nprintf '"+text+"'\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", parent)
	var out bytes.Buffer
	if err := Run(context.Background(), "", []string{"PATH=" + local}, nil, &out, io.Discard, "python"); err != nil || out.String() != "local" {
		t.Fatalf("out=%s err=%v", &out, err)
	}
}

func TestMissingAndNonExecutableCommandsAreActionable(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{{"missing", 127}, {"not-executable", 126}} {
		path := filepath.Join(t.TempDir(), tc.name)
		if tc.code == 126 {
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		err := Run(context.Background(), "", nil, nil, io.Discard, io.Discard, path)
		var exit *ExitError
		if !errors.As(err, &exit) || exit.Code != tc.code || strings.Contains(err.Error(), "exit status") {
			t.Fatal(err)
		}
	}
}

func TestEnvironmentPathDoesNotExecuteImplicitCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("fixture-command", []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), dir, []string{"PATH=."}, nil, io.Discard, io.Discard, "fixture-command")
	if !errors.Is(err, exec.ErrDot) || !strings.Contains(err.Error(), "implicitly") {
		t.Fatal(err)
	}
}

func TestOutputRetainedOnFailure(t *testing.T) {
	out, err := Output(context.Background(), "", "/bin/sh", "-c", "printf data; exit 1")
	if err == nil || string(out) != "data" {
		t.Fatalf("out=%s err=%v", out, err)
	}
}
