// Package runner is the common subprocess boundary. Shell evaluation is never
// implicit: commands and hooks are argv arrays or executable files.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

func command(ctx context.Context, dir string, env []string, args []string) *exec.Cmd {
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Dir = dir
	if env != nil {
		c.Env = env
	}
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = 3 * time.Second
	return c
}

func Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	c := command(ctx, dir, nil, args)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w%s", strings.Join(args, " "), err, detail(stderr.String()))
	}
	return out, nil
}

func detail(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return ": " + s
	}
	return ""
}

func Run(ctx context.Context, dir string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	c := command(ctx, dir, env, args)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	if err := c.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code := exit.ExitCode()
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				code = 128 + int(status.Signal())
			}
			return &ExitError{Code: code, Err: fmt.Errorf("%s: %w", args[0], err)}
		}
		var path *os.PathError
		if errors.Is(err, exec.ErrNotFound) || errors.As(err, &path) {
			return &ExitError{Code: 127, Err: err}
		}
		return err
	}
	return nil
}
