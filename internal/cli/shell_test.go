package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const shellPrelude = `set -eu
eval "$("$WT_BINARY" shell-init "$WT_SHELL")"
`

func TestShellAutocd(t *testing.T) {
	source, err := filepath.Abs("../../cmd/worktreectl")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the initializer's quoting as well as directory paths with spaces.
	binary := filepath.Join(t.TempDir(), "worktreectl's binary")
	if out, err := exec.Command("go", "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s is not installed", shell)
			}
			cases := []struct {
				name, script, config, env, mainName string
				rejectBranchDeletion                bool
			}{
				{name: "new_and_rm", script: `
worktreectl new feature --no-setup
[ "$PWD" = "$WT_ROOT/feature" ]
mkdir nested
builtin cd nested
worktreectl rm
[ "$PWD" = "$WT_MAIN" ]
`},
				{name: "branch_shorthand_and_rm", script: `
worktreectl new fixes/my-feature --no-setup
[ "$PWD" = "$WT_ROOT/fixes-my-feature" ]
[ "$(git symbolic-ref --short HEAD)" = fixes/my-feature ]
[ ! -e "$WT_ROOT/fixes" ]
worktreectl rm fixes/my-feature
[ "$PWD" = "$WT_MAIN" ]
[ ! -e "$WT_ROOT/fixes-my-feature" ]
`},
				{name: "aliases_and_other_worktree", script: `
worktreectl start first --no-setup
[ "$PWD" = "$WT_ROOT/first" ]
worktreectl start second --no-setup
[ "$PWD" = "$WT_ROOT/second" ]
worktreectl done first
[ "$PWD" = "$WT_MAIN" ]
`},
				{name: "config_disable_and_flag_override", config: "[worktreectl]\nauto_cd = false\n", script: `
worktreectl new first --no-setup
[ "$PWD" = "$WT_MAIN" ]
builtin cd "$WT_ROOT/first"
worktreectl new second --no-setup
[ "$PWD" = "$WT_ROOT/first" ]
worktreectl rm second
[ "$PWD" = "$WT_ROOT/first" ]
worktreectl new third --no-setup --no-cd=false
[ "$PWD" = "$WT_ROOT/third" ]
worktreectl rm third --no-cd=false
[ "$PWD" = "$WT_MAIN" ]
`},
				{name: "environment_disable", config: "[worktreectl]\nauto_cd = true\n", env: "0", script: `
worktreectl new first --no-setup
[ "$PWD" = "$WT_MAIN" ]
builtin cd "$WT_ROOT/first"
worktreectl new second --no-setup
worktreectl rm second
[ "$PWD" = "$WT_ROOT/first" ]
`},
				{name: "flag_disable", script: `
worktreectl new first --no-setup --no-cd
[ "$PWD" = "$WT_MAIN" ]
worktreectl new second --no-setup
[ "$PWD" = "$WT_ROOT/second" ]
worktreectl remove first --no-cd
[ "$PWD" = "$WT_ROOT/second" ]
`},
				{name: "non_interactive_disable", script: `
worktreectl new first --no-setup --non-interactive
[ "$PWD" = "$WT_MAIN" ]
worktreectl new second --no-setup
worktreectl rm first --non-interactive
[ "$PWD" = "$WT_ROOT/second" ]
`},
				{name: "dry_run_and_refusal", script: `
worktreectl new first --no-setup
worktreectl rm first --dry-run
[ "$PWD" = "$WT_ROOT/first" ]
printf work > untracked.txt
if worktreectl rm first; then exit 90; else [ "$?" = 3 ]; fi
[ "$PWD" = "$WT_ROOT/first" ]
`},
				{name: "failed_setup", config: "[worktreectl]\nsetup = ['/bin/sh', '-c', 'exit 9']\n", script: `
if worktreectl new first; then exit 90; else [ "$?" = 9 ]; fi
[ "$PWD" = "$WT_MAIN" ]
[ -d "$WT_ROOT/first" ]
`},
				{name: "recover_after_branch_deletion_failure", rejectBranchDeletion: true, script: `
worktreectl new first --no-setup
if worktreectl rm first; then exit 90; else [ "$?" = 1 ]; fi
[ "$PWD" = "$WT_MAIN" ]
[ ! -d "$WT_ROOT/first" ]
`},
				{name: "command_substitution", script: `
created="$(worktreectl new first --no-setup)"
[ "$created" = "$WT_ROOT/first" ]
[ "$PWD" = "$WT_MAIN" ]
`},
				{name: "child_output_and_exit_code", script: `
worktreectl new first --no-setup -- /bin/sh -c 'printf "child-output\n"'
[ "$PWD" = "$WT_ROOT/first" ]
if worktreectl new failed --no-setup -- /bin/sh -c 'exit 17'; then exit 90; else [ "$?" = 17 ]; fi
[ "$PWD" = "$WT_ROOT/first" ]
`},
				{name: "json_stdout", script: `
worktreectl new first --no-setup --json > "$WT_MAIN/new.json"
[ "$PWD" = "$WT_ROOT/first" ]
worktreectl rm first --json > "$WT_MAIN/rm.json"
[ "$PWD" = "$WT_MAIN" ]
cat "$WT_MAIN/new.json"
`},
				{name: "literal_paths_and_trailing_newline", mainName: "repo ' $(touch SHELL_INJECTION)\n", script: `
worktreectl new first --no-setup --no-copy
[ "$PWD" = "$WT_ROOT/first" ]
worktreectl rm first
[ "$PWD" = "$WT_MAIN" ]
[ ! -e "$WT_MAIN/SHELL_INJECTION" ]
`},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					name := tc.mainName
					if name == "" {
						name = "repo with space"
					}
					main, root := fixture(t, name)
					if tc.rejectBranchDeletion {
						hook := `#!/bin/sh
if [ "$1" = prepared ]; then
    while read -r old new ref; do
        if [ "$ref" = refs/heads/wt/first ]; then
            case "$new" in *[!0]*) ;; *) exit 1 ;; esac
        fi
    done
fi
exit 0
`
						if err := os.WriteFile(filepath.Join(main, ".git", "hooks", "reference-transaction"), []byte(hook), 0755); err != nil {
							t.Fatal(err)
						}
					}
					if tc.config != "" {
						write(t, filepath.Join(main, ".devcli.toml"), tc.config)
					}
					if tc.env != "" {
						t.Setenv("WORKTREECTL_AUTO_CD", tc.env)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					c := exec.CommandContext(ctx, shell, "-c", shellPrelude+tc.script)
					c.Dir = main
					c.Env = append(os.Environ(), "WT_BINARY="+binary, "WT_SHELL="+shell, "WT_MAIN="+main, "WT_ROOT="+root)
					var out, logs bytes.Buffer
					c.Stdout, c.Stderr = &out, &logs
					if err := c.Run(); err != nil {
						t.Fatalf("shell integration: %v\nstdout: %s\nstderr: %s", err, out.String(), logs.String())
					}
					switch tc.name {
					case "new_and_rm", "literal_paths_and_trailing_newline":
						created := "first"
						if tc.name == "new_and_rm" {
							created = "feature"
						}
						if out.String() != filepath.Join(root, created)+"\n"+main+"\n" {
							t.Fatal(out.String())
						}
					case "child_output_and_exit_code":
						if out.String() != "child-output\n" {
							t.Fatalf("child stdout changed: %s", out.String())
						}
					case "json_stdout":
						if decode(t, out.String())["data"].(map[string]any)["path"] != filepath.Join(root, "first") {
							t.Fatal(out.String())
						}
						data, err := os.ReadFile(filepath.Join(main, "rm.json"))
						if err != nil {
							t.Fatal(err)
						}
						if decode(t, string(data))["data"].(map[string]any)["main_path"] != main {
							t.Fatal(string(data))
						}
					}
				})
			}
		})
	}
}

func TestShellInitValidation(t *testing.T) {
	for _, args := range [][]string{{"shell-init", "fish"}, {"shell-init", "bash", "--json"}} {
		out, _ := run(t, 2, args...)
		if strings.Contains(strings.Join(args, " "), "--json") {
			decode(t, out)
		}
	}
	out, _ := run(t, 0, "shell-init", "bash")
	if !strings.Contains(out, "worktreectl()") || !strings.Contains(out, "builtin cd") {
		t.Fatal(out)
	}
}

func TestPlainBinaryPathOutputAndNoParentChdir(t *testing.T) {
	main, root := fixture(t)
	out, _ := run(t, 0, "new", "plain", "--no-setup")
	if out != filepath.Join(root, "plain")+"\n" {
		t.Fatal(out)
	}
	if cwd, err := os.Getwd(); err != nil || cwd != main {
		t.Fatalf("binary changed cwd: %s, %v", cwd, err)
	}
	out, _ = run(t, 0, "rm", "plain")
	if out != main+"\n" {
		t.Fatal(out)
	}
	if cwd, err := os.Getwd(); err != nil || cwd != main {
		t.Fatalf("binary changed cwd: %s, %v", cwd, err)
	}
}
