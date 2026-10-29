// Package repo handles Git discovery, worktree records, and path/ref resolution.
package repo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arno-vel-bept/worktreectl/internal/runner"
)

type Worktree struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	Head     string `json:"head"`
	Main     bool   `json:"main"`
	Bare     bool   `json:"bare,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
	Prunable bool   `json:"prunable,omitempty"`
}

type Repo struct {
	Main    string
	Current string
	Common  string
	Base    string
}

func Discover(ctx context.Context, cwd string) (*Repo, error) {
	out, err := runner.Output(ctx, cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not inside a working Git checkout: %w", err)
	}
	// Git terminates path output with one newline; other trailing whitespace
	// can be part of the directory name and must survive shell integration.
	r := &Repo{Current: Physical(strings.TrimSuffix(string(out), "\n"))}
	trees, err := r.Trees(ctx)
	if err != nil || len(trees) == 0 {
		return nil, fmt.Errorf("cannot discover Git worktrees: %w", err)
	}
	if trees[0].Bare {
		return nil, fmt.Errorf("bare repositories are not supported; use a repository with a main checkout")
	}
	r.Main = Physical(trees[0].Path)
	out, err = r.Git(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	r.Common = strings.TrimSuffix(string(out), "\n")
	if !filepath.IsAbs(r.Common) {
		r.Common = filepath.Join(r.Main, r.Common)
	}
	out, err = r.Git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		out, err = r.Git(ctx, "rev-parse", "HEAD")
	}
	if err != nil {
		return nil, fmt.Errorf("repository needs an initial commit: %w", err)
	}
	r.Base = strings.TrimSpace(string(out))
	return r, nil
}

func (r *Repo) Git(ctx context.Context, args ...string) ([]byte, error) {
	dir := r.Main
	if dir == "" {
		dir = r.Current
	}
	return runner.Output(ctx, dir, append([]string{"git"}, args...)...)
}

func (r *Repo) Trees(ctx context.Context) ([]Worktree, error) {
	out, err := r.Git(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return ParseTrees(out), nil
}

// NUL records preserve whitespace, newlines and Git's quoted-path edge cases.
func ParseTrees(out []byte) []Worktree {
	trees := []Worktree{}
	var w Worktree
	flush := func() {
		if w.Path != "" {
			w.Name = filepath.Base(w.Path)
			w.Main = len(trees) == 0
			trees = append(trees, w)
			w = Worktree{}
		}
	}
	for _, field := range bytes.Split(out, []byte{0}) {
		s := string(field)
		key, value, _ := strings.Cut(s, " ")
		switch key {
		case "":
			flush()
		case "worktree":
			flush()
			w.Path = value
		case "HEAD":
			w.Head = value
		case "branch":
			w.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "bare":
			w.Bare = true
		case "locked":
			w.Locked = true
		case "prunable":
			w.Prunable = true
		}
	}
	flush()
	return trees
}

func (r *Repo) Resolve(ctx context.Context, target, prefix string) (Worktree, error) {
	if target == "" {
		target = r.Current
	}
	trees, err := r.Trees(ctx)
	if err != nil {
		return Worktree{}, err
	}
	abs, _ := filepath.Abs(target)
	// Exact paths win over potentially ambiguous names and branches.
	for _, w := range trees {
		if Physical(w.Path) == Physical(abs) {
			return w, nil
		}
	}
	matches := []Worktree{}
	for _, w := range trees {
		if w.Name == target || w.Branch == target || (w.Branch != "" && w.Branch == prefix+target) {
			matches = append(matches, w)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Worktree{}, fmt.Errorf("ambiguous worktree %q; use its full path", target)
	}
	return Worktree{}, fmt.Errorf("no worktree matches %q (see: worktreectl list)", target)
}

func (r *Repo) Dirty(ctx context.Context, path string) (bool, error) {
	out, err := runner.Output(ctx, path, "git", "--no-optional-locks", "status", "--porcelain", "-z", "--untracked-files=all")
	return len(out) > 0, err
}

func (r *Repo) Merged(ctx context.Context, head, base string) (bool, error) {
	// rev-list distinguishes a negative ancestry result from an invalid ref.
	out, err := r.Git(ctx, "rev-list", "--count", base+".."+head, "--")
	return strings.TrimSpace(string(out)) == "0", err
}

func Physical(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	// Roots and new worktree paths often don't exist yet. Resolve the nearest
	// existing ancestor as well, notably macOS's /var -> /private/var alias.
	for parent := abs; ; parent = filepath.Dir(parent) {
		if p, err := filepath.EvalSymlinks(parent); err == nil {
			rel, _ := filepath.Rel(parent, abs)
			return filepath.Join(p, rel)
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	return abs
}

func Under(path, root string) bool {
	rel, err := filepath.Rel(Physical(root), Physical(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (r *Repo) Exclude(pattern string) error {
	path := filepath.Join(r.Common, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == pattern {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(data) > 0 && data[len(data)-1] != '\n' {
		pattern = "\n" + pattern
	}
	_, err = fmt.Fprintln(f, pattern)
	return err
}
