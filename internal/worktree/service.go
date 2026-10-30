// Package worktree coordinates creation, environment/setup, and safe cleanup.
package worktree

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/arno-vel-bept/worktreectl/internal/config"
	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
	"github.com/arno-vel-bept/worktreectl/internal/docker"
	"github.com/arno-vel-bept/worktreectl/internal/process"
	"github.com/arno-vel-bept/worktreectl/internal/repo"
	"github.com/arno-vel-bept/worktreectl/internal/runner"
)

// Refusal distinguishes a safety decision from an operational failure.
type Refusal struct{ Message string }

func (e *Refusal) Error() string              { return e.Message }
func refuse(format string, args ...any) error { return &Refusal{Message: fmt.Sprintf(format, args...)} }
func refuseHint(message, hint string) error {
	return diagnostic.New(message, hint, &Refusal{Message: message})
}

// Service connects repository/configuration state to filesystem and subprocess
// operations. The CLI owns output formatting and shell directory changes.
type Service struct {
	Repo   *repo.Repo
	Config config.Settings
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Piped  bool
	Log    func(string, ...any)
}

// Lock serializes mutations by worktreectl instances in the same repository.
// The returned unlock function must run before an arbitrary user command starts.
// Read-only commands and dry runs must not acquire this lock.
func (s *Service) Lock(ctx context.Context) (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.Repo.Common, "worktreectl.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type NewOptions struct {
	Name, Base, Branch             string
	Detach, Fetch, NoCopy, NoSetup bool
}

type NewResult struct {
	repo.Worktree
	Port int `json:"port"`
}

func (s *Service) New(ctx context.Context, o NewOptions) (NewResult, error) {
	if o.Name == "" {
		o.Name = "wt-" + time.Now().Format("0102-150405.000000")
	}
	requestedName := o.Name
	name, branch, err := s.creationNames(ctx, o)
	if err != nil {
		return NewResult{}, err
	}
	o.Name = name
	path := filepath.Join(s.Config.Root, o.Name)
	if repo.Under(s.Config.Root, s.Repo.Common) {
		return NewResult{}, refuseHint("the worktree root is inside Git's metadata directory", "Choose a dedicated root outside .git using WORKTREECTL_ROOT or root in .devcli.toml.")
	}
	if _, err := os.Lstat(path); err == nil {
		if strings.Contains(requestedName, "/") {
			trees, err := s.Repo.Trees(ctx)
			if err != nil {
				return NewResult{}, diagnostic.New(fmt.Sprintf("directory %s already exists, and existing Git checkouts could not be inspected", path), "Check Git's repository permissions and run worktreectl list; nothing was overwritten.", err)
			}
			for _, w := range trees {
				if w.Branch == branch {
					return NewResult{}, branchInUse(branch, w.Path)
				}
			}
			return NewResult{}, refuseHint(fmt.Sprintf("cannot create worktree: branch %q maps to directory %q, but %s already exists", branch, name, path), fmt.Sprintf("Nothing was overwritten. Use worktreectl list to inspect existing checkouts, or choose another directory: worktreectl new another-name --branch %s.", quoteValue(branch)))
		}
		return NewResult{}, refuseHint(fmt.Sprintf("cannot create worktree: %s already exists", path), "Choose a different name, or use worktreectl list to find and enter the existing checkout.")
	} else if !os.IsNotExist(err) {
		return NewResult{}, diagnostic.New(fmt.Sprintf("cannot inspect the intended worktree directory %s", path), "Make sure the worktree root is a directory, with accessible and writable parent directories.", err)
	}
	if o.Fetch {
		s.Log("fetching repository")
		if _, err := s.Repo.Git(ctx, "fetch", "--prune"); err != nil {
			return NewResult{}, diagnostic.New("could not fetch the repository before worktree creation", "Check the remote URL with git remote -v, your credentials, and the network connection; no checkout was created. Git reported: "+strings.TrimPrefix(err.Error(), "git failed: "), err)
		}
	}
	if o.Base == "" {
		out, err := runner.Output(ctx, s.Repo.Current, "git", "rev-parse", "HEAD")
		if err != nil {
			return NewResult{}, err
		}
		o.Base = strings.TrimSpace(string(out))
	}
	if strings.HasPrefix(o.Base, "-") {
		return NewResult{}, diagnostic.New(fmt.Sprintf("invalid base ref %q", o.Base), "Use --base with an existing branch, tag, or commit; it cannot start with a hyphen.", nil)
	}
	if _, err := s.Repo.Git(ctx, "rev-parse", "--verify", "--end-of-options", o.Base+"^{commit}"); err != nil {
		return NewResult{}, diagnostic.New(fmt.Sprintf("base %q does not name a commit", o.Base), "Check the branch or tag name; add --fetch if you need a remote branch.", err)
	}
	if !o.Detach {
		trees, err := s.Repo.Trees(ctx)
		if err != nil {
			return NewResult{}, err
		}
		for _, w := range trees {
			if w.Branch == branch {
				return NewResult{}, branchInUse(branch, w.Path)
			}
		}
		if err := s.checkBranchNamespace(ctx, branch); err != nil {
			return NewResult{}, err
		}
	}
	port, err := s.pickPort(ctx, o.Name)
	if err != nil {
		return NewResult{}, err
	}
	if repo.Under(s.Config.Root, s.Repo.Main) {
		rel, _ := filepath.Rel(s.Repo.Main, s.Config.Root)
		if rel == "." || strings.ContainsAny(rel, "\n\r*?[]") {
			return NewResult{}, refuseHint("the worktree root must be a dedicated directory without Git glob characters", "Set root in .devcli.toml or WORKTREECTL_ROOT to a dedicated directory, preferably beside the main checkout.")
		}
		if err := s.Repo.Exclude("/" + filepath.ToSlash(rel) + "/"); err != nil {
			return NewResult{}, err
		}
	}
	if err := s.Repo.Exclude("/.worktree.env"); err != nil {
		return NewResult{}, err
	}
	// These disposable dependency directories are removed with the checkout,
	// even when the project's .gitignore does not mention them.
	for _, dir := range []string{".venv", "venv", "node_modules", ".worktree-venvs"} {
		if err := s.Repo.Exclude(dir + "/"); err != nil {
			return NewResult{}, err
		}
		// Git treats a symlink as a file, so the directory-only pattern above
		// does not cover an explicitly reused node_modules link at the root.
		if err := s.Repo.Exclude("/" + dir); err != nil {
			return NewResult{}, err
		}
	}
	if err := os.MkdirAll(s.Config.Root, 0755); err != nil {
		return NewResult{}, diagnostic.New(fmt.Sprintf("cannot create the worktree root %s", s.Config.Root), "Check directory permissions and available disk space, or choose a writable WORKTREECTL_ROOT.", err)
	}
	args := []string{"worktree", "add"}
	if o.Detach {
		args = append(args, "--detach", path, o.Base)
		branch = ""
	} else if _, err := s.Repo.Git(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		s.Log("reusing branch %s (--base ignored)", branch)
		args = append(args, path, branch)
	} else {
		args = append(args, "--no-track", "-b", branch, path, o.Base)
	}
	if _, err := s.Repo.Git(ctx, args...); err != nil {
		return NewResult{}, diagnostic.New(fmt.Sprintf("Git could not create the worktree at %s", path), "Check directory permissions, available space, branch/ref conflicts, and git worktree list before retrying. Git reported: "+strings.TrimPrefix(err.Error(), "git failed: "), err)
	}
	if strings.Contains(requestedName, "/") {
		s.Log("branch %s uses worktree directory %s", branch, name)
	}
	// Do not roll back a worktree after a failed hook/copy: a hook may already
	// have created user data. Leave a clear recovery path instead.
	fail := func(err error) (NewResult, error) {
		result := NewResult{Worktree: repo.Worktree{Name: o.Name, Path: path, Branch: branch}, Port: port}
		hint := diagnostic.Hint(err)
		if hint != "" {
			hint += " "
		}
		hint += fmt.Sprintf("The checkout was kept for inspection. After fixing setup, run your setup command there; to discard it, preview worktreectl rm %s --dry-run first.", o.Name)
		return result, diagnostic.New(fmt.Sprintf("worktree created at %s, but setup did not finish: %s", path, err), hint, err)
	}
	if err := s.writeEnv(path, o.Name, branch, port); err != nil {
		return fail(err)
	}
	if !o.NoCopy {
		if err := s.copyIncludes(path); err != nil {
			return fail(err)
		}
	}
	if !o.NoSetup {
		found, err := s.hook(ctx, "setup", path, false)
		if err != nil {
			return fail(err)
		}
		if !found && s.Config.AutoSetup {
			if err := s.autoSetup(ctx, path); err != nil {
				return fail(err)
			}
		}
	}
	w, err := s.Repo.Resolve(ctx, path, s.Config.BranchPrefix)
	if err != nil {
		return fail(err)
	}
	s.Log("ready: %s (branch %s, port %d)", path, branch, port)
	return NewResult{Worktree: w, Port: port}, nil
}

func (s *Service) Exec(ctx context.Context, path string, args []string, jsonMode bool) error {
	if len(args) == 0 {
		args = []string{os.Getenv("SHELL")}
		if args[0] == "" {
			args[0] = "/bin/sh"
		}
	}
	env, err := environment(path)
	if err != nil {
		return err
	}
	stdout := s.Stdout
	if jsonMode {
		stdout = s.Stderr
	}
	return runner.Run(ctx, path, env, s.Stdin, stdout, s.Stderr, args...)
}

func (s *Service) hook(ctx context.Context, name, path string, dry bool) (bool, error) {
	args := s.Config.Setup
	if name == "teardown" {
		args = s.Config.Teardown
	}
	if len(args) == 0 {
		for _, dir := range []string{path, s.Repo.Main} {
			h := filepath.Join(dir, ".worktreectl", name)
			info, err := os.Stat(h)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return true, err
			}
			if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return true, diagnostic.New(fmt.Sprintf("setup/teardown hook %s is not executable", h), "Add a shebang (for example #!/bin/sh) and run chmod +x on the hook, or configure an argv command in .devcli.toml.", nil)
			}
			args = []string{h}
			break
		}
	}
	if len(args) == 0 {
		return false, nil
	}
	s.Log("%s%s: %s", dryPrefix(dry), name, strings.Join(args, " "))
	if dry {
		return true, nil
	}
	env, err := environment(path)
	if err != nil {
		return true, err
	}
	return true, runner.Run(ctx, path, env, s.Stdin, s.Stderr, s.Stderr, args...)
}

func dryPrefix(dry bool) string {
	if dry {
		return "[dry-run] "
	}
	return ""
}

type Activity struct {
	Worktree          repo.Worktree      `json:"worktree"`
	Processes         []process.Process  `json:"processes"`
	Skipped           []process.Process  `json:"protected_processes"`
	Containers        []docker.Container `json:"containers"`
	DockerAvailable   bool               `json:"docker_available"`
	ProcessesReliable bool               `json:"processes_reliable"`
}

func (s *Service) snapshots(ctx context.Context) (process.Snapshot, docker.Snapshot, error) {
	p, err := process.Capture(ctx)
	if err != nil {
		return p, docker.Snapshot{}, err
	}
	if p.Warning != "" {
		s.Log("warning: %s", p.Warning)
	}
	d, err := docker.Capture(ctx, s.Config.NoDocker)
	if d.Warning != "" {
		s.Log("warning: %s", d.Warning)
	}
	return p, d, err
}

func (s *Service) activity(w repo.Worktree, p process.Snapshot, d docker.Snapshot) Activity {
	matched, skipped := p.Match(w.Path, os.Getpid(), s.Config.MatchCmdline, s.Piped)
	return Activity{Worktree: w, Processes: matched, Skipped: skipped, Containers: d.Match(w.Path), DockerAvailable: d.Available, ProcessesReliable: p.Reliable}
}

func (s *Service) PS(ctx context.Context, w repo.Worktree) (Activity, error) {
	p, d, err := s.snapshots(ctx)
	if err != nil {
		return Activity{}, err
	}
	a := s.activity(w, p, d)
	for i := range a.Processes {
		a.Processes[i].Ports = process.Ports(ctx, a.Processes[i].PID)
	}
	return a, nil
}

type ListEntry struct {
	repo.Worktree
	State             string `json:"state"`
	Ahead             *int   `json:"ahead"`
	Processes         int    `json:"processes"`
	ProcessesReliable bool   `json:"processes_reliable"`
}

func (s *Service) List(ctx context.Context) ([]ListEntry, error) {
	trees, err := s.Repo.Trees(ctx)
	if err != nil {
		return nil, err
	}
	p, err := process.Capture(ctx)
	if err != nil {
		return nil, err
	}
	if p.Warning != "" {
		s.Log("warning: %s", p.Warning)
	}
	result := []ListEntry{}
	for _, w := range trees {
		e := ListEntry{Worktree: w, State: "missing", ProcessesReliable: p.Reliable}
		if _, err := os.Stat(w.Path); err == nil {
			dirty, err := s.Repo.Dirty(ctx, w.Path)
			if err != nil {
				return nil, err
			}
			e.State = "clean"
			if dirty {
				e.State = "dirty"
			}
			out, err := s.Repo.Git(ctx, "rev-list", "--count", s.Repo.Base+".."+w.Head, "--")
			if err != nil {
				return nil, err
			}
			var ahead int
			if _, err := fmt.Sscan(string(out), &ahead); err != nil {
				return nil, err
			}
			e.Ahead = &ahead
			matched, _ := p.Match(w.Path, os.Getpid(), s.Config.MatchCmdline, s.Piped)
			e.Processes = len(matched)
		}
		result = append(result, e)
	}
	return result, nil
}
