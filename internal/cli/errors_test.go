package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommonRepositoryErrorsAreShortAndActionable(t *testing.T) {
	for _, kind := range []string{"not a repo", "no commits", "bare"} {
		t.Run(kind, func(t *testing.T) {
			path := t.TempDir()
			want := "not in a Git repository"
			switch kind {
			case "no commits":
				git(t, path, "init", "--initial-branch=main")
				want = "no initial commit"
			case "bare":
				git(t, path, "init", "--bare", "--initial-branch=main")
				want = "bare Git repository"
			}
			t.Chdir(path)
			out, logs := run(t, 1, "list")
			if out != "" || !strings.Contains(logs, want) || !strings.Contains(logs, "hint:") || strings.Contains(logs, "exit status") || strings.Contains(logs, "fatal:") {
				t.Fatalf("stdout=%q stderr=%q", out, logs)
			}
			out, _ = run(t, 1, "list", "--json")
			err := decode(t, out)["error"].(map[string]any)
			if !strings.Contains(err["message"].(string), want) || err["hint"] == "" {
				t.Fatal(err)
			}
		})
	}
}

func TestGitMissingIsNotMisreportedAsNotARepository(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())
	out, _ := run(t, 1, "list", "--json")
	err := decode(t, out)["error"].(map[string]any)
	if !strings.Contains(err["message"].(string), "Git is not installed") || !strings.Contains(err["hint"].(string), "Git 2.31") {
		t.Fatal(err)
	}
}

func TestCommonCreationErrorsHaveRecoveryHints(t *testing.T) {
	fixture(t)
	run(t, 0, "new", "existing", "--no-setup")
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"new", "existing", "--no-setup"}, 3, "already exists"},
		{[]string{"new", "other", "--branch", "wt/existing", "--no-setup"}, 3, "already checked out"},
		{[]string{"new", "other", "--base", "missing-ref", "--no-setup"}, 1, "does not name a commit"},
		{[]string{"new", "bad name", "--no-setup"}, 1, "invalid worktree name"},
		{[]string{"path", "missing"}, 1, "no worktree matches"},
		{[]string{"rm"}, 3, "main checkout"},
		{[]string{"list", "--bad-flag"}, 2, "unknown flag"},
		{[]string{"list", "--config", "missing-config.toml"}, 1, "does not exist"},
		{[]string{"completion"}, 2, "missing argument"},
	} {
		out, _ := run(t, tc.code, append(tc.args, "--json")...)
		err := decode(t, out)["error"].(map[string]any)
		if !strings.Contains(err["message"].(string), tc.want) || err["hint"] == nil || err["hint"] == "" {
			t.Fatal(tc.args, err)
		}
	}
}

func TestFailedSetupJSONIncludesKeptCheckoutAndRecovery(t *testing.T) {
	main, root := fixture(t)
	write(t, filepath.Join(main, "pyproject.toml"), "[tool.poetry]\npackage-mode = false\n")
	git(t, main, "add", "pyproject.toml")
	git(t, main, "commit", "-m", "python fixture")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	mocks := t.TempDir()
	if err := os.WriteFile(filepath.Join(mocks, "git"), []byte(fmt.Sprintf("#!/bin/sh\nexec %q \"$@\"\n", realGit)), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", mocks)
	out, _ := run(t, 127, "new", "missing-poetry", "--json")
	result := decode(t, out)
	if result["data"].(map[string]any)["path"] != filepath.Join(root, "missing-poetry") {
		t.Fatal(result)
	}
	problem := result["error"].(map[string]any)
	if !strings.Contains(problem["message"].(string), "poetry") || !strings.Contains(problem["hint"].(string), "kept for inspection") {
		t.Fatal(problem)
	}
}
