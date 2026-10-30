package worktree

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
)

var unsafeDirectoryChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

const maxBranchDirectoryName = 128

// Branch syntax is validated BEFORE this conversion. Never repair an invalid
// ref by normalizing it, or pass the user's branch name to filepath.Join.
func branchDirectoryName(branch string) string {
	name := unsafeDirectoryChars.ReplaceAllString(branch, "-")
	name = strings.TrimLeft(name, "._-")
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(branch)))[:10]
	if name == "" {
		return "branch-" + digest
	}
	if len(name) > maxBranchDirectoryName {
		return name[:maxBranchDirectoryName-len(digest)-1] + "-" + digest
	}
	return name
}

func (s *Service) creationNames(ctx context.Context, o NewOptions) (string, string, error) {
	name, branch := o.Name, o.Branch
	shorthand := strings.Contains(name, "/")
	if o.Detach && branch != "" {
		return "", "", diagnostic.New("--detach and --branch cannot be used together", "Choose a branch, or use --detach with --base REF for a detached checkout.", nil)
	}
	if shorthand {
		if o.Detach {
			return "", "", diagnostic.New("a slash-containing name selects a branch, so it cannot be used with --detach", "Use a flat worktree name with --detach; --base REF can select an existing branch or commit.", nil)
		}
		if branch != "" && branch != name {
			return "", "", diagnostic.New("the branch shorthand and --branch select different branches", "Omit --branch to keep the slash-containing branch name, or use a flat directory name with --branch BRANCH.", nil)
		}
		branch = name
	} else if !validName.MatchString(name) {
		return "", "", diagnostic.New(fmt.Sprintf("invalid worktree name %q", name), "Use letters, digits, dots, underscores, or hyphens; start with a letter or digit. A name containing / is interpreted as an exact Git branch instead of a directory path.", nil)
	}
	if o.Detach {
		return name, "", nil
	}
	if branch == "" {
		branch = s.Config.BranchPrefix + name
	}
	if _, err := s.Repo.Git(ctx, "check-ref-format", "refs/heads/"+branch); err != nil || strings.HasPrefix(branch, "-") {
		hint := "Use a valid Git branch name such as fixes/my-feature-name. Avoid spaces, leading hyphens, leading/trailing or repeated slashes, '..', .lock suffixes, dot-prefixed components, and Git's reserved ref characters."
		if !shorthand && o.Branch == "" {
			hint += " Check branch_prefix in .devcli.toml, or choose a branch explicitly with --branch."
		}
		return "", "", diagnostic.New(fmt.Sprintf("invalid Git branch name %q", branch), hint, err)
	}
	if shorthand {
		name = branchDirectoryName(branch)
	}
	return name, branch, nil
}

func (s *Service) checkBranchNamespace(ctx context.Context, branch string) error {
	out, err := s.Repo.Git(ctx, "for-each-ref", "--format=%(refname:strip=2)", "refs/heads/")
	if err != nil {
		return diagnostic.New("cannot inspect existing Git branches before creation", "Check Git's repository permissions and retry; no checkout was created.", err)
	}
	for _, existing := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if existing == "" || existing == branch {
			continue
		}
		if strings.HasPrefix(branch, existing+"/") || strings.HasPrefix(existing, branch+"/") {
			return refuseHint(fmt.Sprintf("branch %q conflicts with existing branch %q", branch, existing), "Git cannot store both a branch and slash-separated child branches under the same name. Choose a different branch/prefix, or explicitly rename the existing branch before retrying.")
		}
	}
	return nil
}

func branchInUse(branch, path string) error {
	return refuseHint(fmt.Sprintf("branch %q is already checked out at %s", branch, path), fmt.Sprintf("Use worktreectl path %s to find that checkout. For an independent checkout, choose a different branch; changing only the directory name cannot check out the same branch twice.", quoteValue(branch)))
}
