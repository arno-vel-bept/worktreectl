package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchShorthandPreservesBranchAndUsesFlatDirectory(t *testing.T) {
	main, root := fixture(t)
	write(t, filepath.Join(main, ".devcli.toml"), "[worktreectl]\nbranch_prefix = 'custom/'\n")
	unicodeName := "branch-" + fmt.Sprintf("%x", sha256.Sum256([]byte("修复/功能")))[:10]
	longBranch := "long/" + strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100) + "/" + strings.Repeat("c", 100)
	longName := strings.ReplaceAll(longBranch, "/", "-")[:117] + "-" + fmt.Sprintf("%x", sha256.Sum256([]byte(longBranch)))[:10]
	for _, tc := range []struct{ command, branch, directory string }{
		{"new", "fixes/my-feature-name", "fixes-my-feature-name"},
		{"start", "team/fixes/another-feature", "team-fixes-another-feature"},
		{"new", "_fixes/my-feature", "fixes-my-feature"},
		{"new", "fixes/my'feature+$HOME", "fixes-my-feature-HOME"},
		{"new", "修复/功能", unicodeName},
		{"new", longBranch, longName},
	} {
		out, _ := run(t, 0, tc.command, tc.branch, "--no-setup", "--json")
		data := decode(t, out)["data"].(map[string]any)
		path := filepath.Join(root, tc.directory)
		if data["name"] != tc.directory || data["path"] != path || data["branch"] != tc.branch {
			t.Fatal(data)
		}
		if actual := git(t, path, "symbolic-ref", "--short", "HEAD"); actual != tc.branch {
			t.Fatal("branch was rewritten", actual)
		}
		out, _ = run(t, 0, "exec", tc.branch, "--", "/bin/sh", "-c", `printf '%s\n%s' "$WORKTREE_NAME" "$WORKTREE_BRANCH"`)
		if out != tc.directory+"\n"+tc.branch {
			t.Fatal("worktree environment lost directory/branch identity", out)
		}
		out, _ = run(t, 0, "path", tc.branch)
		if out != path+"\n" {
			t.Fatal("branch lookup used the branch as a path", out)
		}
		first, _, _ := strings.Cut(tc.branch, "/")
		if _, err := os.Lstat(filepath.Join(root, first)); !os.IsNotExist(err) {
			t.Fatal("branch prefix became a nested worktree directory", err)
		}
	}
	run(t, 0, "rm", "fixes/my-feature-name", "--dry-run")
	if _, err := os.Stat(filepath.Join(root, "fixes-my-feature-name")); err != nil {
		t.Fatal("dry run removed the flat directory", err)
	}
	run(t, 0, "rm", "fixes/my-feature-name")
	if _, err := os.Stat(filepath.Join(root, "fixes-my-feature-name")); !os.IsNotExist(err) {
		t.Fatal("cleanup left the flat directory behind", err)
	}
}

func TestBranchDirectoryCollisionDoesNotFollowLinksOrReplaceFiles(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			main, root := fixture(t)
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "fixes-feature")
			outside := t.TempDir()
			write(t, filepath.Join(outside, "marker"), "keep target")
			if kind == "file" {
				write(t, path, "keep existing file")
			} else if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			before := git(t, main, "for-each-ref", "--format=%(refname)", "refs/heads/")
			out, _ := run(t, 3, "new", "fixes/feature", "--no-setup", "--json")
			if !strings.Contains(decode(t, out)["error"].(map[string]any)["hint"].(string), "Nothing was overwritten") {
				t.Fatal(out)
			}
			if kind == "file" {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "keep existing file" {
					t.Fatal(string(data), err)
				}
			} else if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("existing dependency/directory link was replaced", info, err)
			}
			if data, err := os.ReadFile(filepath.Join(outside, "marker")); err != nil || string(data) != "keep target" {
				t.Fatal("collision followed a link outside the worktree root", string(data), err)
			}
			if after := git(t, main, "for-each-ref", "--format=%(refname)", "refs/heads/"); after != before {
				t.Fatal("collision created a Git branch", after)
			}
		})
	}
}

func TestInvalidBranchIsRejectedBeforeFetch(t *testing.T) {
	main, root := fixture(t)
	git(t, main, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
	out, logs := run(t, 1, "new", "fixes/../escape", "--fetch", "--no-setup", "--json")
	if !strings.Contains(decode(t, out)["error"].(map[string]any)["message"].(string), "invalid Git branch name") || strings.Contains(logs, "fetching") {
		t.Fatal("invalid branch was processed after fetch", out, logs)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("invalid fetched creation touched the worktree root", err)
	}
	out, _ = run(t, 1, "new", "fixes/valid", "--fetch", "--no-setup", "--json")
	problem := decode(t, out)["error"].(map[string]any)
	if !strings.Contains(problem["message"].(string), "could not fetch") || !strings.Contains(problem["hint"].(string), "git remote -v") {
		t.Fatal("fetch failure omitted recovery instructions", problem)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("failed fetch created a checkout", err)
	}
}

func TestInvalidSlashBranchNamesDoNotCreatePathsOrRefs(t *testing.T) {
	main, root := fixture(t)
	before := git(t, main, "for-each-ref", "--format=%(refname)", "refs/heads/")
	for _, name := range []string{"../escape", "/absolute/path", "fixes//feature", "fixes/feature/", "fixes/../feature", "fixes/.hidden", "fixes/feature.lock", "fixes/name with space", "fixes/feature\\name", "fixes/feature@{1}", "fixes/name\nline"} {
		out, _ := run(t, 1, "new", name, "--no-setup", "--json")
		problem := decode(t, out)["error"].(map[string]any)
		if !strings.Contains(problem["message"].(string), "invalid Git branch name") || problem["hint"] == "" || strings.Contains(out, "fatal:") {
			t.Fatal("unhelpful error", problem)
		}
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatal("invalid branch created a worktree root", name, err)
		}
	}
	if after := git(t, main, "for-each-ref", "--format=%(refname)", "refs/heads/"); after != before {
		t.Fatal("invalid branch input modified Git refs", after)
	}
	out, logs := run(t, 1, "new", "fixes/../escape", "--no-setup")
	if out != "" || !strings.Contains(logs, "invalid Git branch name") || !strings.Contains(logs, "hint:") || strings.Contains(logs, "fatal:") {
		t.Fatal("invalid branch did not produce a friendly human error", out, logs)
	}
	for _, args := range [][]string{
		{"new", "fixes/feature", "--detach", "--json"},
		{"new", "fixes/feature", "--branch", "other/feature", "--json"},
	} {
		out, _ := run(t, 2, args...)
		if decode(t, out)["error"].(map[string]any)["hint"] == "" {
			t.Fatal(out)
		}
	}
}

func TestSanitizedDirectoryCollisionsAreRefusedWithRecovery(t *testing.T) {
	main, root := fixture(t)
	run(t, 0, "new", "fixes/my-feature", "--no-setup")
	path := filepath.Join(root, "fixes-my-feature")
	write(t, filepath.Join(path, "user-work"), "keep this")
	before := git(t, main, "for-each-ref", "--format=%(refname)", "refs/heads/")
	out, _ := run(t, 3, "new", "fixes-my/feature", "--no-setup", "--json")
	problem := decode(t, out)["error"].(map[string]any)
	if !strings.Contains(problem["message"].(string), "maps to directory") || !strings.Contains(problem["hint"].(string), "--branch 'fixes-my/feature'") {
		t.Fatal(problem)
	}
	if data, err := os.ReadFile(filepath.Join(path, "user-work")); err != nil || string(data) != "keep this" {
		t.Fatal("collision overwrote existing work", string(data), err)
	}
	if after := git(t, main, "for-each-ref", "--format=%(refname)", "refs/heads/"); after != before {
		t.Fatal("collision created a branch", after)
	}
	run(t, 0, "new", "separate-directory", "--branch", "fixes-my/feature", "--no-setup")
	if branch := git(t, filepath.Join(root, "separate-directory"), "symbolic-ref", "--short", "HEAD"); branch != "fixes-my/feature" {
		t.Fatal("collision recovery changed branch identity", branch)
	}
}

func TestGitBranchNamespaceConflictsAreExplainedBeforeCreation(t *testing.T) {
	main, root := fixture(t)
	git(t, main, "branch", "fixes/feature/child")
	before := git(t, main, "for-each-ref", "--format=%(refname)", "refs/heads/")
	for _, name := range []string{"main/feature", "fixes/feature"} {
		out, _ := run(t, 3, "new", name, "--no-setup", "--json")
		problem := decode(t, out)["error"].(map[string]any)
		if !strings.Contains(problem["message"].(string), "conflicts with existing branch") || !strings.Contains(problem["hint"].(string), "Git cannot store both") {
			t.Fatal(problem)
		}
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatal("namespace conflict created a path", err)
		}
	}
	if after := git(t, main, "for-each-ref", "--format=%(refname)", "refs/heads/"); after != before {
		t.Fatal("namespace refusal changed refs", after)
	}
}

func TestBranchShorthandReusesExistingBranchAndKeepsFailedSetup(t *testing.T) {
	main, root := fixture(t)
	git(t, main, "branch", "fixes/existing")
	write(t, filepath.Join(main, ".devcli.toml"), "[worktreectl]\nsetup = ['/bin/sh', '-c', 'exit 9']\n")
	out, _ := run(t, 9, "new", "fixes/existing", "--branch", "fixes/existing", "--json")
	result := decode(t, out)
	data := result["data"].(map[string]any)
	if data["path"] != filepath.Join(root, "fixes-existing") || data["name"] != "fixes-existing" || data["branch"] != "fixes/existing" {
		t.Fatal(data)
	}
	if !strings.Contains(result["error"].(map[string]any)["hint"].(string), "worktreectl rm fixes-existing --dry-run") {
		t.Fatal("recovery hint reused the slash-containing directory input", result)
	}
	out, _ = run(t, 3, "new", "fixes/existing", "--no-setup", "--json")
	problem := decode(t, out)["error"].(map[string]any)
	if !strings.Contains(problem["message"].(string), "already checked out") || !strings.Contains(problem["hint"].(string), "worktreectl path 'fixes/existing'") {
		t.Fatal("repeated shorthand gave an impossible directory-only recovery", problem)
	}
	out, _ = run(t, 3, "new", "another-directory", "--branch", "fixes/existing", "--no-setup", "--json")
	if !strings.Contains(decode(t, out)["error"].(map[string]any)["message"].(string), "already checked out") {
		t.Fatal(out)
	}
	write(t, filepath.Join(main, ".devcli.toml"), "[worktreectl]\nauto_setup = false\n")
	run(t, 0, "rm", "fixes/existing")
}
