package worktree

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
	"github.com/arno-vel-bept/worktreectl/internal/docker"
	"github.com/arno-vel-bept/worktreectl/internal/process"
	"github.com/arno-vel-bept/worktreectl/internal/repo"
	"github.com/arno-vel-bept/worktreectl/internal/runner"
)

type RemoveOptions struct {
	DryRun, Force, KeepBranch, Volumes bool
	RequireIdle                        bool
}

type CleanupResult struct {
	Path       string          `json:"path"`
	MainPath   string          `json:"main_path,omitempty"`
	DryRun     bool            `json:"dry_run"`
	Actions    []docker.Action `json:"actions"`
	BranchKept bool            `json:"branch_kept"`
}

func (s *Service) notMain(w repo.Worktree) error {
	if w.Main || repo.Physical(w.Path) == repo.Physical(s.Repo.Main) {
		return refuseHint(fmt.Sprintf("%s is the main checkout; it cannot be stopped or removed", s.Repo.Main), "Run worktreectl list, then select a linked checkout with worktreectl rm NAME or worktreectl stop NAME.")
	}
	return nil
}

func (s *Service) safeSnapshots(ctx context.Context) (process.Snapshot, docker.Snapshot, error) {
	p, d, err := s.snapshots(ctx)
	if err != nil {
		return p, d, err
	}
	if !p.Reliable {
		return p, d, refuseHint("cannot safely clean up: process discovery is incomplete", p.Warning+" Stop the reported services yourself and restore process discovery before retrying.")
	}
	if d.Warning != "" {
		return p, d, refuseHint("cannot safely clean up while Docker is unreachable", "Start Docker and retry, or explicitly pass --no-docker to leave all Docker resources untouched.")
	}
	return p, d, nil
}

func (s *Service) stopPlan(w repo.Worktree, p process.Snapshot, d docker.Snapshot, remove, volumes bool) ([]docker.Action, []process.Process, error) {
	matched, skipped := p.Match(w.Path, os.Getpid(), s.Config.MatchCmdline, s.Piped)
	if len(skipped) > 0 {
		s.Log("warning: leaving %d caller process-group member(s) running", len(skipped))
		if remove {
			return nil, nil, refuse("worktree has protected process-group members; stop them manually before removing it")
		}
	}
	actions := []docker.Action{}
	for _, target := range matched {
		actions = append(actions, docker.Action{Description: fmt.Sprintf("stop process %d: %s", target.PID, target.Command), Args: []string{"kill", "-TERM", fmt.Sprint(target.PID)}})
	}
	dockerActions, err := d.Plan(w.Path, remove, volumes)
	if err != nil {
		return nil, nil, refuse("%s", err)
	}
	return append(actions, dockerActions...), matched, nil
}

func (s *Service) executeStop(ctx context.Context, path string, actions []docker.Action, targets []process.Process, dry bool) error {
	for _, action := range actions {
		s.Log("%s%s", dryPrefix(dry), action.Description)
	}
	if dry {
		return nil
	}
	var env []string
	for _, action := range actions {
		if len(action.Args) > 0 && action.Args[0] == "docker" {
			var err error
			env, err = environment(path)
			if err != nil {
				return err
			}
			break
		}
	}
	if err := process.Stop(ctx, targets, time.Duration(s.Config.KillTimeout)*time.Second); err != nil {
		return err
	}
	for _, action := range actions {
		if len(action.Args) > 0 && action.Args[0] == "docker" {
			// No destructive fallback: a failed Compose command must not silently
			// remove containers or volumes using a broader operation.
			if err := runner.Run(ctx, path, env, nil, s.Stderr, s.Stderr, action.Args...); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Stop(ctx context.Context, w repo.Worktree, dry bool) (CleanupResult, error) {
	if err := s.notMain(w); err != nil {
		return CleanupResult{}, err
	}
	p, d, err := s.safeSnapshots(ctx)
	if err != nil {
		return CleanupResult{}, err
	}
	actions, targets, err := s.stopPlan(w, p, d, false, false)
	if err != nil {
		return CleanupResult{}, err
	}
	result := CleanupResult{Path: w.Path, DryRun: dry, Actions: actions, BranchKept: true}
	return result, s.executeStop(ctx, w.Path, actions, targets, dry)
}

func (s *Service) checkRemove(ctx context.Context, w repo.Worktree, o RemoveOptions) error {
	if err := s.notMain(w); err != nil {
		return err
	}
	if w.Locked && !o.Force {
		return refuseHint(fmt.Sprintf("worktree %s is locked", w.Path), fmt.Sprintf("Check why it was locked before running git worktree unlock %s. --force overrides the lock and may discard work.", quoteValue(w.Path)))
	}
	if _, err := os.Stat(w.Path); err != nil {
		return diagnostic.New(fmt.Sprintf("worktree %s is missing or inaccessible", w.Path), "Check the path and permissions. If it was already deleted, inspect git worktree prune --dry-run before pruning its registration.", err)
	}
	if o.Force {
		return nil
	}
	dirty, err := s.Repo.Dirty(ctx, w.Path)
	if err != nil {
		return err
	}
	if dirty {
		return refuseHint(fmt.Sprintf("worktree %s has uncommitted changes", w.Path), fmt.Sprintf("Inspect them with git -C %s status, then commit or stash them (including untracked files). Use --force only if you intend to discard them.", quoteValue(w.Path)))
	}
	if w.Branch == "" {
		merged, err := s.Repo.Merged(ctx, w.Head, s.Repo.Base)
		if err != nil {
			return err
		}
		out, err := s.Repo.Git(ctx, "for-each-ref", "--contains", w.Head, "--format=%(refname)", "refs/heads/")
		if err != nil {
			return err
		}
		if !merged && strings.TrimSpace(string(out)) == "" {
			return refuse("detached HEAD has commits on no branch; save them with git -C %s switch -c <branch>, or pass --force", quoteValue(w.Path))
		}
	}
	return nil
}

func (s *Service) Remove(ctx context.Context, w repo.Worktree, o RemoveOptions) (CleanupResult, error) {
	if err := s.checkRemove(ctx, w, o); err != nil {
		return CleanupResult{}, err
	}
	p, d, err := s.safeSnapshots(ctx)
	if err != nil {
		return CleanupResult{}, err
	}
	if o.RequireIdle {
		a := s.activity(w, p, d)
		if len(a.Processes)+len(a.Skipped) > 0 {
			return CleanupResult{}, refuse("processes became active during gc")
		}
		for _, c := range a.Containers {
			if c.Running {
				return CleanupResult{}, refuse("containers became active during gc")
			}
		}
	}
	actions, targets, err := s.stopPlan(w, p, d, true, o.Volumes)
	if err != nil {
		return CleanupResult{}, err
	}
	if _, err := s.hook(ctx, "teardown", w.Path, o.DryRun); err != nil {
		return CleanupResult{}, err
	}
	if !o.DryRun {
		w, err = s.Repo.Resolve(ctx, w.Path, s.Config.BranchPrefix)
		if err != nil {
			return CleanupResult{}, err
		}
		if err := s.checkRemove(ctx, w, o); err != nil {
			return CleanupResult{}, err
		}
		// Hooks can launch additional processes/containers. Discover again rather
		// than relying on the pre-hook snapshot.
		p, d, err = s.safeSnapshots(ctx)
		if err != nil {
			return CleanupResult{}, err
		}
		actions, targets, err = s.stopPlan(w, p, d, true, o.Volumes)
		if err != nil {
			return CleanupResult{}, err
		}
	}
	if o.RequireIdle {
		a := s.activity(w, p, d)
		if len(a.Processes)+len(a.Skipped) > 0 {
			return CleanupResult{}, refuse("processes became active during gc")
		}
		for _, c := range a.Containers {
			if c.Running {
				return CleanupResult{}, refuse("containers became active during gc")
			}
		}
		merged, err := s.Repo.Merged(ctx, w.Head, s.Repo.Base)
		if err != nil {
			return CleanupResult{}, err
		}
		if !merged {
			return CleanupResult{}, refuse("commits changed during gc")
		}
	}
	branchKept := w.Branch != ""
	deleteBranch := false
	if w.Branch != "" && !o.KeepBranch {
		merged, err := s.Repo.Merged(ctx, w.Head, s.Repo.Base)
		if err != nil {
			return CleanupResult{}, err
		}
		deleteBranch = o.Force || merged
		branchKept = !deleteBranch
	}
	if deleteBranch {
		trees, err := s.Repo.Trees(ctx)
		if err != nil {
			return CleanupResult{}, err
		}
		for _, other := range trees {
			if other.Branch == w.Branch && repo.Physical(other.Path) != repo.Physical(w.Path) {
				deleteBranch, branchKept = false, true
				s.Log("warning: kept branch %s because it is checked out in %s", w.Branch, other.Path)
				break
			}
		}
	}
	if err := s.executeStop(ctx, w.Path, actions, targets, o.DryRun); err != nil {
		return CleanupResult{}, err
	}
	args := []string{"git", "worktree", "remove"}
	if o.Force {
		args = append(args, "--force", "--force")
	}
	args = append(args, w.Path)
	actions = append(actions, docker.Action{Description: "remove worktree " + w.Path, Args: args})
	s.Log("%sremoving worktree %s", dryPrefix(o.DryRun), w.Path)
	if !o.DryRun {
		if _, err := s.Repo.Git(ctx, args[1:]...); err != nil {
			return CleanupResult{}, err
		}
	}
	if deleteBranch {
		// Compare-and-delete avoids losing commits if another Git client updates
		// the branch after our ancestry check. Shared branch config is left intact.
		args = []string{"git", "update-ref", "-d", "refs/heads/" + w.Branch, w.Head}
		actions = append(actions, docker.Action{Description: "delete branch " + w.Branch, Args: args})
		if !o.DryRun {
			if _, err := s.Repo.Git(ctx, args[1:]...); err != nil {
				return CleanupResult{}, err
			}
		}
		s.Log("%sdeleted branch %s", dryPrefix(o.DryRun), w.Branch)
	} else if branchKept {
		s.Log("kept branch %s", w.Branch)
	}
	if repo.Under(s.Repo.Current, w.Path) {
		s.Log("your shell was inside the removed worktree; cd %s", quoteValue(s.Repo.Main))
	}
	return CleanupResult{Path: w.Path, MainPath: s.Repo.Main, DryRun: o.DryRun, Actions: actions, BranchKept: branchKept}, nil
}

type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type GCResult struct {
	DryRun  bool            `json:"dry_run"`
	Removed []CleanupResult `json:"removed"`
	Skipped []Skipped       `json:"skipped"`
}

func (s *Service) GC(ctx context.Context, dry, all bool) (GCResult, error) {
	result := GCResult{DryRun: dry, Removed: []CleanupResult{}, Skipped: []Skipped{}}
	trees, err := s.Repo.Trees(ctx)
	if err != nil {
		return result, err
	}
	p, d, err := s.safeSnapshots(ctx)
	if err != nil {
		return result, err
	}
	for _, w := range trees {
		if w.Main || (!all && !repo.Under(w.Path, s.Config.Root)) {
			continue
		}
		reason := ""
		switch {
		case w.Locked:
			reason = "locked"
		case w.Prunable:
			reason = "missing/prunable; inspect git worktree prune --dry-run"
		default:
			dirty, err := s.Repo.Dirty(ctx, w.Path)
			if err != nil {
				return result, err
			}
			merged, err := s.Repo.Merged(ctx, w.Head, s.Repo.Base)
			if err != nil {
				return result, err
			}
			a := s.activity(w, p, d)
			switch {
			case dirty:
				reason = "uncommitted changes"
			case !merged:
				reason = "commits not in " + s.Repo.Base
			case len(a.Processes)+len(a.Skipped) > 0:
				reason = "processes still running"
			default:
				for _, c := range a.Containers {
					if c.Running {
						reason = "containers still running"
						break
					}
				}
			}
		}
		if reason != "" {
			s.Log("keep %s: %s", w.Name, reason)
			result.Skipped = append(result.Skipped, Skipped{Path: w.Path, Reason: reason})
			continue
		}
		r, err := s.Remove(ctx, w, RemoveOptions{DryRun: dry, RequireIdle: true})
		if err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, r)
	}
	s.Log("%sgc: %d worktree(s) removed", dryPrefix(dry), len(result.Removed))
	return result, nil
}
