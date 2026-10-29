package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, names ...string) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := "repo with space"
	if len(names) > 0 {
		name = names[0]
	}
	main := filepath.Join(base, name)
	if err := os.Mkdir(main, 0755); err != nil {
		t.Fatal(err)
	}
	git(t, main, "init", "--initial-branch=main")
	git(t, main, "config", "user.name", "Test")
	git(t, main, "config", "user.email", "test@example.invalid")
	git(t, main, "config", "commit.gpgsign", "false")
	write(t, filepath.Join(main, "README.md"), "fixture\n")
	write(t, filepath.Join(main, ".gitignore"), ".env\n")
	git(t, main, "add", ".")
	git(t, main, "commit", "-m", "initial")
	for _, name := range []string{"ROOT", "BASE", "BRANCH_PREFIX", "PORT_BASE", "KILL_TIMEOUT", "MATCH_CMDLINE", "NO_DOCKER", "AUTO_CD", "REUSE_DEPENDENCIES"} {
		t.Setenv("WORKTREECTL_"+name, "")
	}
	root := filepath.Join(base, "trees")
	t.Setenv("WORKTREECTL_ROOT", root)
	t.Setenv("WORKTREECTL_NO_DOCKER", "1")
	t.Setenv("NO_COLOR", "1")
	t.Chdir(main)
	return main, root
}

func run(t *testing.T, want int, args ...string) (string, string) {
	t.Helper()
	var out, logs bytes.Buffer
	code := Execute(context.Background(), args, strings.NewReader(""), &out, &logs, BuildInfo{Version: "test", Commit: "abc", Date: "today"})
	if code != want {
		t.Fatalf("%v: code %d, want %d\nout: %s\nlogs: %s", args, code, want, out.String(), logs.String())
	}
	return out.String(), logs.String()
}

func decode(t *testing.T, out string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(out))
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		t.Fatalf("invalid JSON: %s: %v", out, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("extra stdout after JSON: %s", out)
	}
	return data
}

func TestLifecycleAndJSON(t *testing.T) {
	main, root := fixture(t)
	write(t, filepath.Join(main, ".env"), "TOKEN=private\n")
	out, _ := run(t, 0, "new", "feature", "--no-setup", "--json")
	d := decode(t, out)["data"].(map[string]any)
	path := filepath.Join(root, "feature")
	if d["path"] != path || d["branch"] != "wt/feature" {
		t.Fatal(d)
	}
	data, err := os.ReadFile(filepath.Join(path, ".env"))
	if err != nil || string(data) != "TOKEN=private\n" {
		t.Fatal(string(data), err)
	}
	info, err := os.Stat(filepath.Join(path, ".worktree.env"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if status := git(t, path, "status", "--porcelain"); status != "" {
		t.Fatalf("generated metadata is not excluded: %s", status)
	}
	out, _ = run(t, 0, "path", "wt/feature")
	if out != path+"\n" {
		t.Fatal(out)
	}
	out, _ = run(t, 0, "exec", "feature", "--", "/bin/sh", "-c", `printf '%s:%s' "$WORKTREE_NAME" "$PORT"`)
	if !strings.HasPrefix(out, "feature:") {
		t.Fatal(out)
	}
	out, logs := run(t, 17, "exec", "feature", "--json", "--", "/bin/sh", "-c", "printf child-output; exit 17")
	if !strings.Contains(logs, "child-output") || strings.Contains(out, "child-output") {
		t.Fatalf("stdout=%s stderr=%s", out, logs)
	}
	decode(t, out)
	out, _ = run(t, 0, "list", "--json")
	if len(decode(t, out)["data"].([]any)) != 2 {
		t.Fatal(out)
	}
	out, _ = run(t, 0, "rm", "feature", "--dry-run", "--json")
	if decode(t, out)["data"].(map[string]any)["dry_run"] != true {
		t.Fatal(out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("dry run removed worktree")
	}
	write(t, filepath.Join(path, "untracked.txt"), "work\n")
	out, _ = run(t, 3, "rm", "feature", "--json")
	decode(t, out)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("refusal removed worktree")
	}
	run(t, 0, "rm", "feature", "--force", "--keep-branch")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("worktree not removed")
	}
	git(t, main, "show-ref", "--verify", "refs/heads/wt/feature")
}

func TestSafetyAndArgumentErrors(t *testing.T) {
	fixture(t)
	for _, command := range []string{"rm", "stop"} {
		out, _ := run(t, 3, command, "--json")
		decode(t, out)
	}
	for _, args := range [][]string{
		{"unknown", "--json"}, {"list", "extra", "--json"}, {"new", "one", "two", "--json"},
		{"list", "--invalid", "--json"}, {"new", "x", "--detach", "--branch", "b", "--json"},
		{"exec", "--non-interactive", "--json"}, {"version", "--color", "bogus", "--json"},
	} {
		out, _ := run(t, 2, args...)
		decode(t, out)
	}
	for _, args := range [][]string{{"version", "--json"}, {"--version", "--json"}, {"--help", "--json"}, {"help", "new", "--json"}, {"--json"}} {
		out, _ := run(t, 0, args...)
		decode(t, out)
	}
	out, _ := run(t, 0, "completion", "zsh")
	if !strings.Contains(out, "#compdef worktreectl") {
		t.Fatal("no completion script")
	}
}

func TestConfigurationDiscoveredFromMain(t *testing.T) {
	main, root := fixture(t)
	write(t, filepath.Join(main, ".devcli.toml"), "[worktreectl]\nbranch_prefix = 'task/'\nauto_setup = false\n")
	run(t, 0, "new", "configured")
	t.Chdir(filepath.Join(root, "configured"))
	out, _ := run(t, 0, "path", "configured", "--json")
	if decode(t, out)["data"].(map[string]any)["branch"] != "task/configured" {
		t.Fatal(out)
	}
	run(t, 0, "new", "from-linked", "--no-setup", "--no-copy")
	out, _ = run(t, 0, "path", "from-linked", "--json")
	if decode(t, out)["data"].(map[string]any)["branch"] != "task/from-linked" {
		t.Fatal(out)
	}
}

func TestRemovePreservesUnmergedBranchAndDetachedCommits(t *testing.T) {
	main, root := fixture(t)
	run(t, 0, "new", "unmerged", "--no-setup")
	path := filepath.Join(root, "unmerged")
	write(t, filepath.Join(path, "feature.txt"), "commit\n")
	git(t, path, "add", ".")
	git(t, path, "commit", "-m", "feature")
	run(t, 0, "rm", "unmerged")
	git(t, main, "show-ref", "--verify", "refs/heads/wt/unmerged")
	run(t, 0, "new", "detached", "--detach", "--no-setup")
	path = filepath.Join(root, "detached")
	write(t, filepath.Join(path, "detached.txt"), "commit\n")
	git(t, path, "add", ".")
	git(t, path, "commit", "-m", "detached")
	run(t, 3, "rm", "detached")
	git(t, path, "switch", "-c", "saved-detached")
	run(t, 0, "rm", "detached")
	git(t, main, "show-ref", "--verify", "refs/heads/saved-detached")
}

func TestGCSkipsDirtyAndLocked(t *testing.T) {
	main, root := fixture(t)
	for _, name := range []string{"clean", "dirty", "locked"} {
		run(t, 0, "new", name, "--no-setup")
	}
	write(t, filepath.Join(root, "dirty", "untracked"), "dirty\n")
	git(t, main, "worktree", "lock", filepath.Join(root, "locked"))
	out, _ := run(t, 0, "gc", "--json")
	d := decode(t, out)["data"].(map[string]any)
	if len(d["removed"].([]any)) != 1 || len(d["skipped"].([]any)) != 2 {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(root, "dirty")); err != nil {
		t.Fatal(err)
	}
}

func TestHooksFailLoudlyAndDryRunNeverExecutes(t *testing.T) {
	main, root := fixture(t)
	write(t, filepath.Join(main, ".devcli.toml"), "[worktreectl]\nsetup = ['/bin/sh', '-c', 'echo setup-log; exit 9']\n")
	_, logs := run(t, 9, "new", "failed", "--json")
	if !strings.Contains(logs, "setup-log") {
		t.Fatal(logs)
	}
	path := filepath.Join(root, "failed")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed hook discarded worktree")
	}
	marker := filepath.Join(main, "teardown-ran")
	write(t, filepath.Join(main, ".devcli.toml"), "[worktreectl]\nteardown = ['/bin/sh', '-c', 'touch \"$WORKTREE_MAIN/teardown-ran\"']\n")
	run(t, 0, "rm", "failed", "-n")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("dry run executed teardown")
	}
	run(t, 0, "rm", "failed")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("teardown didn't run")
	}
}

func TestRealBinaryStopsOnlyWorktreeProcess(t *testing.T) {
	// Use a real CLI subprocess so the test-owned sleeper is not protected as
	// a descendant of the CLI itself.
	source, err := filepath.Abs("../../cmd/worktreectl")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "worktreectl")
	build := exec.Command("go", "build", "-o", binary, source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	main, root := fixture(t)
	run(t, 0, "new", "live", "--no-setup")
	sleeper := exec.Command("sleep", "60")
	sleeper.Dir = filepath.Join(root, "live")
	sleeper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, binary, "stop", "live", "--json")
	c.Dir = main
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("stop: %s: %v", out, err)
	}
	done := make(chan error, 1)
	go func() { done <- sleeper.Wait() }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("worktree process was not stopped")
	}
	if _, err := os.Stat(filepath.Join(root, "live")); err != nil {
		t.Fatal("stop removed files")
	}
}

func TestDryRunDoesNotCreateLock(t *testing.T) {
	main, _ := fixture(t)
	run(t, 0, "new", "preview", "--no-setup")
	lock := filepath.Join(main, ".git", "worktreectl.lock")
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	run(t, 0, "rm", "preview", "--dry-run")
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("dry run created repository lock")
	}
}

func TestDockerUnavailableBlocksCleanupUnlessExplicitlyDisabled(t *testing.T) {
	_, root := fixture(t)
	run(t, 0, "new", "docker", "--no-setup")
	mocks := t.TempDir()
	path := filepath.Join(mocks, "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", mocks+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WORKTREECTL_NO_DOCKER", "0")
	out, _ := run(t, 3, "rm", "docker", "--json")
	if len(decode(t, out)["warnings"].([]any)) == 0 {
		t.Fatal("missing Docker warning")
	}
	if _, err := os.Stat(filepath.Join(root, "docker")); err != nil {
		t.Fatal("refusal removed worktree")
	}
	run(t, 0, "rm", "docker", "--no-docker")
}

func TestTeardownCannotDiscardNewDetachedCommits(t *testing.T) {
	main, root := fixture(t)
	run(t, 0, "new", "teardown-commit", "--detach", "--no-setup")
	write(t, filepath.Join(main, ".devcli.toml"), "[worktreectl]\nteardown = ['git', '-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-m', 'from teardown']\n")
	run(t, 3, "rm", "teardown-commit")
	if _, err := os.Stat(filepath.Join(root, "teardown-commit")); err != nil {
		t.Fatal("detached teardown commit was discarded")
	}
}
