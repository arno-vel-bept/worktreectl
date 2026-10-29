# Command and configuration reference

Already installed? This is the detailed guide to commands, repository settings, shell integration, and cleanup safety. Start with the [README](../README.md) for installation and a quick walkthrough.

Requires Git 2.31+ and macOS or Linux (amd64/arm64). macOS process discovery requires `lsof`; on Linux, readable `/proc` working directories are required. Docker and Compose v2 are optional.

## Quick start

```sh
eval "$(worktreectl shell-init zsh)" # Use bash for Bash; add to your shell startup file.
worktreectl new feature --no-setup # Automatically enters the new worktree.
worktreectl exec feature -- npm run dev
worktreectl list
worktreectl ps feature
worktreectl stop feature --dry-run
worktreectl rm feature --dry-run
worktreectl rm feature            # Automatically returns to the main checkout.
```

Names can be a worktree directory name, full path, or branch. Omit the name to use the checkout containing the current directory. Ambiguous names require a full path.

| Command | Purpose |
| --- | --- |
| `new [NAME] [-- CMD...]` | Create a branch/worktree, copy local files, generate environment, run setup |
| `exec [NAME] [-- CMD...]` | Run a command with the worktree environment; defaults to `$SHELL` on a terminal |
| `path [NAME]` | Print just the path |
| `list` | Show branches, clean/dirty state, commits ahead of the base, and process counts |
| `ps [NAME]` | Show processes, listening TCP ports, and associated containers |
| `stop [NAME]` | Stop associated processes and containers; keep the checkout |
| `rm [NAME]` | Run teardown, stop/remove resources, remove the checkout, optionally delete its branch |
| `gc` | Remove only clean, fully merged, idle, unlocked worktrees under the configured root |
| `version` | Print build information |
| `completion SHELL` | Generate Bash, Zsh, Fish, or PowerShell completions |
| `shell-init SHELL` | Generate the small Bash/Zsh wrapper needed for autocd |

Existing aliases remain: `start`, `enter`, `ls`, `status`, `remove`, and `done`.

Useful flags:

- `new`: `--base REF`, `--branch NAME`, `--detach`, `--fetch`, `--no-copy`, `--no-setup`.
- `stop`, `rm`, `gc`: `-n / --dry-run` (no hooks, signals, or filesystem writes).
- `rm`: `-f / --force`, `--keep-branch`, `--volumes`.
- `gc`: `--all` to include worktrees outside the configured root.
- Global: `--json`, `--non-interactive`, `--config FILE`, `--no-docker`, `--no-cd`, `--color auto|always|never`.

Use `--` before subprocess arguments so their flags belong to the subprocess, not this CLI. `new` defaults to the **current checkout's HEAD**, not the cleanup base. An existing branch is reused; `--base` then has no effect.

### Automatic directory changes

Autocd is **enabled by default** once shell integration is initialized:

```sh
# Add ONE of these to your startup file:
eval "$(worktreectl shell-init zsh)"  # ~/.zshrc
eval "$(worktreectl shell-init bash)" # ~/.bashrc
```

`new` enters the new checkout after successful setup (and after `-- CMD` finishes successfully). `rm` returns to the main checkout after removal, even if you removed a different worktree. Aliases behave the same. Dry runs and failed creation/removal do not change directory; if removal succeeds but subsequent branch deletion fails, the shell still returns to the main checkout and preserves the failure exit code.

Disable both behaviors per repository with:

```toml
[worktreectl]
auto_cd = false
```

Or use `WORKTREECTL_AUTO_CD=0` globally, or `--no-cd` for one invocation. Explicit `--no-cd=false` can re-enable autocd over configuration/environment defaults. `--non-interactive` always disables autocd.

A binary cannot change its parent shell's directory: the initializer defines a tiny shell function that calls the Go binary and applies a literal directory returned over a separate pipe. It does **not** evaluate command output, create temporary files, or alter stdout/JSON. All worktree logic remains in Go. Direct binary calls (`command worktreectl ...` or `./bin/worktreectl ...`) only print paths; commands inside pipelines, command substitutions, or subshells cannot change the outer shell's directory. When building locally, `eval "$(./bin/worktreectl shell-init zsh)"` also works; the generated wrapper uses the binary's absolute path.

## Repository configuration

The same binary works in every repository. It reads the **main checkout's** `.devcli.toml`, including when invoked from a linked worktree. `--config FILE` overrides discovery; an explicit relative config path is relative to your current directory. Settings paths are relative to the main checkout.

```toml
[worktreectl]
root = "../my-project.worktrees"
base = "main"
branch_prefix = "wt/"
port_base = 4000
kill_timeout = 10
auto_setup = false
auto_cd = true
copy = [".env", ".env.*", "config/local.json"]
setup = ["make", "setup"]
teardown = ["make", "teardown"]
```

See [`examples/devcli.toml`](../examples/devcli.toml) for every setting. Only `[worktreectl]` is interpreted; other tools can share the file. This tool stays focused on worktrees rather than introducing unrelated `dev deploy` commands.

Precedence: **explicit command flags → environment → TOML → defaults**. Supported environment overrides:

| Variable | Default |
| --- | --- |
| `WORKTREECTL_ROOT` | `<repo-parent>/<repo>.worktrees` |
| `WORKTREECTL_BASE` | Main checkout's branch, or its commit if detached |
| `WORKTREECTL_BRANCH_PREFIX` | `wt/` |
| `WORKTREECTL_PORT_BASE` | `4000` |
| `WORKTREECTL_KILL_TIMEOUT` | `10` seconds |
| `WORKTREECTL_MATCH_CMDLINE` | `false` |
| `WORKTREECTL_NO_DOCKER` | `false` |
| `WORKTREECTL_AUTO_CD` | `true` (requires shell integration) |

Boolean environment values accept `0`/`1` or `false`/`true`. `--base` applies to creation only; `WORKTREECTL_BASE` / TOML `base` controls ahead counts and cleanup ancestry.

### Local files and setup

- If TOML `copy` is omitted, read `.worktreeinclude` (one checkout-relative glob per line); absent that file, copy `.env` and `.env.*`.
- Blank lines, comments, and `!` entries are skipped. This is simple filesystem globbing, not full gitignore syntax or recursive `**` matching.
- `copy = []` disables copying. Existing destination files/directories are never overwritten. Traversal outside the checkout and `.git` / `.worktree.env` copying are rejected or skipped. Symlinks are copied as links, not followed.
- Each new worktree gets a mode-`0600` `.worktree.env` with `WORKTREE_NAME`, `WORKTREE_PATH`, `WORKTREE_MAIN`, `WORKTREE_BRANCH`, `WORKTREE_PORT`, `PORT`, and a unique-by-worktree `COMPOSE_PROJECT_NAME`. Detached branches are represented by an empty string.
- Ports are deterministically selected in steps of ten, avoiding assigned worktree ports and currently bound loopback ports. Assignment is advisory, not a lasting socket reservation.
- Metadata and any root inside the main checkout are added to the shared Git `info/exclude` file.
- TOML hook commands are **argv arrays**, never implicitly evaluated as shell code. Without configured commands, executable `.worktreectl/setup` or `.worktreectl/teardown` files are used (target checkout first, main checkout second). Hooks need an executable bit and a shebang; the CLI does not assume Bash.
- Without a setup hook, dependency autodetection supports pnpm/Bun/Yarn/npm, uv/Poetry/pip, and Go. Use `--no-setup` or `auto_setup = false` to avoid automatic installs.
- Hooks execute trusted repository code. Inspect unfamiliar repositories before running setup/teardown. Hook or install failures are reported; the created worktree is **kept for inspection**, not rolled back.

`exec` parses `.worktree.env` as literal values instead of sourcing it. Standard single-quoted, double-quoted, and backslash-escaped literals work; shell expansion and the prototype's uncommon Bash ANSI-C `$'…'` quoting are rejected. Existing ordinary prototype-generated files remain compatible.

## Cleanup safety

- The main checkout is never stopped or removed, even with `--force`.
- `rm` refuses uncommitted changes and unreferenced detached commits. Unmerged **named** branches are retained; merged branches are deleted only after checking ancestry against the configured cleanup base. Ref deletion compares the expected commit to avoid deleting concurrent branch updates. Git's branch-specific config is left intact.
- `--force` explicitly permits losing uncommitted work, overriding worktree locks, and deleting unmerged branches. `--keep-branch` still wins.
- Process matching uses working directories inside the worktree. Command-line matching is opt-in. The CLI, its ancestors, and its descendants are always protected; same-process-group members are also protected when output feeds a pipe. Removal refuses if those protected group members still depend on the worktree.
- Signal targets are revalidated by PID/start time/command before `SIGTERM`, then escalated to `SIGKILL` after the configured timeout. Process matching is best-effort: subprocesses that change cwd outside the checkout or outlive discovery can be missed. Repositories can have a teardown hook for such services.
- Containers match Compose working-directory labels or bind mounts under the worktree. Compose projects shared across checkouts are refused. Cleanup failures are surfaced, with no broader destructive fallback. Docker checks time out instead of hanging indefinitely.
- Missing process discovery or an unreachable installed Docker daemon blocks cleanup. `--no-docker` is an explicit opt-out and leaves Docker resources untouched. Other users' inaccessible processes cannot always be inspected; use the same user that started the services.
- `gc` also checks running containers, skips locked/missing worktrees, and never force-removes anything. Missing registrations are left for an explicit `git worktree prune --dry-run` / `git worktree prune`, rather than pruning unrelated records automatically.
- Mutating commands serialize via a repository-local lock. This coordinates worktreectl instances, not independent Git commands or service launchers. Safety is rechecked after teardown hooks. Dry runs don't create the lock.

## Automation and output

By default, `new` and `path` print only the worktree path; successful `rm` prints only the main checkout path. This output is retained when autocd is disabled or shell integration is absent. Inspection commands render tables (plain when redirected). Logs and hook/install output go to stderr. Color is automatic on terminals and respects `NO_COLOR`; `--color never` disables it explicitly.

`--json` emits exactly one envelope on stdout, including errors; subprocess stdout is redirected to stderr. For example:

```sh
worktreectl new ci-check --no-setup --non-interactive --json
worktreectl exec ci-check --json -- go test ./...
worktreectl rm ci-check --dry-run --json
```

```json
{"command":"path","data":{"name":"feature","path":"/code/repo.worktrees/feature","branch":"wt/feature","head":"…","main":false},"warnings":[]}
```

Errors include `error.code` and `error.message`. Partial execution details are included when available. Use `.data.path` for worktree paths, `.data.main_path` for `rm`'s main checkout path, and `.data.exit_code` for `exec` / `new -- CMD` results. Use the JSON fields, not human table formatting, in automation. Completion scripts and shell initialization are raw output and explicitly reject `--json`; help/version support it.

The CLI itself **never prompts**. `--non-interactive` additionally closes stdin to hooks/subprocesses, disallows implicit shells, and disables autocd. Explicit commands normally inherit stdin, so pipes still work.

Exit codes: `0` success, `1` operational failure, `2` invalid CLI usage, `3` safety refusal, `130` interrupted CLI. Subprocess failures propagate their exit code (missing executable: `127`; signal termination: `128 + signal`).

## Installation

See the [installation guide](../README.md#install) for the published [installer Gist](https://gist.github.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6) and [GitHub Releases](https://github.com/arno-vel-bept/worktreectl/releases). The installer detects OS/architecture, verifies SHA-256 before extraction, and atomically installs to `~/.local/bin` without sudo. It prints PATH and shell-integration instructions without editing startup files.

`--bin-dir /absolute/path` customizes the destination. `--repo OWNER/REPO` overrides the default `arno-vel-bept/worktreectl` release repository. Environment equivalents: `WORKTREECTL_REPO`, `WORKTREECTL_VERSION`, `WORKTREECTL_BIN_DIR`. Checksums detect corrupted downloads; they do not authenticate a compromised release account or installer source.

Zsh completions, for example:

```sh
mkdir -p ~/.zsh/completions
worktreectl completion zsh > ~/.zsh/completions/_worktreectl
# In ~/.zshrc, before compinit:
# fpath=(~/.zsh/completions $fpath)
# autoload -Uz compinit && compinit
```

## Development and releases

See [development.md](development.md) for the code layout, formatting checks, tests, release process, and installer Gist maintenance.
