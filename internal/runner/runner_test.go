package runner

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestChildExitCode(t *testing.T) {
	err := Run(context.Background(), "", nil, nil, io.Discard, io.Discard, "/bin/sh", "-c", "exit 17")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 17 {
		t.Fatalf("exit error: %v", err)
	}
}

func TestOutputRetainedOnFailure(t *testing.T) {
	out, err := Output(context.Background(), "", "/bin/sh", "-c", "printf data; exit 1")
	if err == nil || string(out) != "data" {
		t.Fatalf("out=%s err=%v", out, err)
	}
}
