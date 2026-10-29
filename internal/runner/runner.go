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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
)

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

func command(ctx context.Context, dir string, env []string, args []string) *exec.Cmd {
	name := args[0]
	var lookupErr error
	if env != nil && !strings.ContainsRune(name, filepath.Separator) {
		name, lookupErr = environmentCommand(name, env)
	}
	c := exec.CommandContext(ctx, name, args[1:]...)
	if lookupErr != nil {
		c.Err = lookupErr
	}
	c.Dir = dir
	if env != nil {
		c.Env = env
	}
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = 3 * time.Second
	return c
}

// exec.Command resolves PATH before Cmd.Env is applied. Resolve explicitly so
// `exec -- python` actually uses the worktree's virtualenv, not the caller's.
func environmentCommand(name string, env []string) (string, error) {
	path := ""
	var denied bool
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if errors.Is(err, os.ErrPermission) || (err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 == 0) {
			denied = true
		}
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			if !filepath.IsAbs(candidate) {
				return name, &exec.Error{Name: name, Err: exec.ErrDot}
			}
			return candidate, nil
		}
	}
	if denied {
		return name, &exec.Error{Name: name, Err: os.ErrPermission}
	}
	return name, &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return OutputEnv(ctx, dir, nil, args...)
}

func OutputEnv(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("no command was supplied")
	}
	var stderr bytes.Buffer
	c := command(ctx, dir, env, args)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return out, diagnostic.New(fmt.Sprintf("%s failed: %s", args[0], text), "", err)
		}
		return out, commandFailure(args[0], err)
	}
	return out, nil
}

func commandFailure(name string, err error) error {
	var path *os.PathError
	if errors.As(err, &path) && path.Op == "chdir" {
		return diagnostic.New(fmt.Sprintf("cannot enter working directory %q", path.Path), "Check that the directory still exists and is accessible.", err)
	}
	if errors.Is(err, exec.ErrDot) {
		return diagnostic.New(fmt.Sprintf("will not run %q from the current directory implicitly", name), "Use an explicit path such as ./"+name+" if you trust this executable.", err)
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return diagnostic.New(fmt.Sprintf("command %q was not found", name), "Install it and make sure it is available on PATH, then retry.", err)
	}
	if errors.Is(err, os.ErrPermission) {
		return diagnostic.New(fmt.Sprintf("cannot execute %q: permission denied", name), "Check the file's permissions and executable bit.", err)
	}
	if errors.Is(err, syscall.ENOEXEC) {
		return diagnostic.New(fmt.Sprintf("%q is not a runnable program on this machine", name), "Check the binary's platform, or add a valid shebang to the script.", err)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return diagnostic.New(fmt.Sprintf("%s exited with code %d", name, exit.ExitCode()), "See the command's output above for details.", err)
	}
	return fmt.Errorf("could not run %s: %w", name, err)
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
			return &ExitError{Code: code, Err: diagnostic.New(fmt.Sprintf("%s exited with code %d", args[0], code), "See the command's output above for details.", err)}
		}
		var path *os.PathError
		if errors.As(err, &path) && path.Op == "chdir" {
			return diagnostic.New(fmt.Sprintf("cannot enter working directory %q", dir), "Check that the directory still exists and is accessible.", err)
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return &ExitError{Code: 127, Err: commandFailure(args[0], err)}
		}
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.ENOEXEC) {
			return &ExitError{Code: 126, Err: commandFailure(args[0], err)}
		}
		return commandFailure(args[0], err)
	}
	return nil
}
