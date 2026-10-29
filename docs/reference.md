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
| `upgrade` | Upgrade this executable to the latest stable release, without needing a Git repository |
| `completion SHELL` | Generate Bash, Zsh, Fish, or PowerShell completions |
| `shell-init SHELL` | Generate the small Bash/Zsh wrapper needed for autocd |

Existing aliases remain: `start`, `enter`, `ls`, `status`, `remove`, and `done`. `update` aliases `upgrade`.

Useful flags:

- `new`: `--base REF`, `--branch NAME`, `--detach`, `--fetch`, `--no-copy`, `--no-setup`, `--reuse-deps` (v0.2.0).
- `stop`, `rm`, `gc`: `-n / --dry-run` (no hooks, signals, or filesystem writes).
- `rm`: `-f / --force`, `--keep-branch`, `--volumes`.
- `gc`: `--all` to include worktrees outside the configured root.
- `upgrade`: `--check`, `--version vX.Y.Z`, `--repo OWNER/REPO`.
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
| `WORKTREECTL_REUSE_DEPENDENCIES` | `false` (v0.2.0; guarded `node_modules` sharing) |

Boolean environment values accept `0`/`1` or `false`/`true`. `--base` applies to creation only; `WORKTREECTL_BASE` / TOML `base` controls ahead counts and cleanup ancestry.

### Local files and setup

- If TOML `copy` is omitted, read `.worktreeinclude` (one checkout-relative glob per line); absent that file, copy `.env` and `.env.*`.
- Blank lines, comments, and `!` entries are skipped. This is simple filesystem globbing, not full gitignore syntax or recursive `**` matching.
- `copy = []` disables copying. Existing destination files/directories are never overwritten. Traversal outside the checkout and `.git` / `.worktree.env` copying are rejected or skipped. Symlinks are copied as links, not followed.
- Each new worktree gets a mode-`0600` `.worktree.env` with `WORKTREE_NAME`, `WORKTREE_PATH`, `WORKTREE_MAIN`, `WORKTREE_BRANCH`, `WORKTREE_PORT`, `PORT`, and a unique-by-worktree `COMPOSE_PROJECT_NAME`. Detached branches are represented by an empty string.
- Ports are deterministically selected in steps of ten, avoiding assigned worktree ports and currently bound loopback ports. Assignment is advisory, not a lasting socket reservation.
- Metadata, disposable dependency directories (`.venv`, `venv`, `node_modules`, `.worktree-venvs`), and any root inside the main checkout are added to the shared Git `info/exclude` file. This does not hide changes to tracked files.
- TOML hook commands are **argv arrays**, never implicitly evaluated as shell code. Without configured commands, executable `.worktreectl/setup` or `.worktreectl/teardown` files are used (target checkout first, main checkout second). Hooks need an executable bit and a shebang; the CLI does not assume Bash.
- Without a setup hook, dependency autodetection supports pnpm/Bun/Yarn/npm, uv/Poetry/pip, and Go. Use `--no-setup` or `auto_setup = false` to avoid automatic installs.
- Hooks execute trusted repository code. Inspect unfamiliar repositories before running setup/teardown. Hook or install failures are reported; the created worktree is **kept for inspection**, not rolled back.

`exec` parses `.worktree.env` as literal values instead of sourcing it. Standard single-quoted, double-quoted, and backslash-escaped literals work; shell expansion and the prototype's uncommon Bash ANSI-C `$'…'` quoting are rejected. Existing ordinary prototype-generated files remain compatible.

### Dependency setup

Automatic setup runs only when there is no explicit setup command or executable hook. Both Python and JavaScript can be detected in the same checkout. Detection uses the checkout's files, not just which tools happen to be installed. A missing required manager is an actionable error, not a silent switch to npm or pip. Use `--no-setup` / `auto_setup = false` to opt out.

| Project | Automatic setup |
| --- | --- |
| npm | `npm ci` with `package-lock.json` / `npm-shrinkwrap.json`; otherwise `npm install` |
| pnpm | `pnpm install --frozen-lockfile` with a lockfile; otherwise `pnpm install` |
| Yarn | Frozen lockfile on Classic; immutable install on modern Yarn |
| Bun | Frozen install when `bun.lock` / `bun.lockb` exists |
| uv | `uv sync --locked` with `uv.lock`; otherwise `uv sync` when uv metadata/backend is detected |
| Poetry | `poetry install --no-interaction`, detected from its lockfile, `[tool.poetry]`, or Poetry build backend |
| Pipenv | `pipenv sync --dev` with its lockfile; otherwise `pipenv install --dev` |
| Plain Python | Create `.venv` with `python3 -m venv`; use its Python for `-m pip install` |
| Go | `go mod download` |

JavaScript `packageManager` in `package.json` takes precedence over lockfile detection. Conflicting lockfiles without a declaration require you to select a manager. pnpm/Yarn can run through Corepack if their standalone command is unavailable. npm/pnpm workspaces install normally from the workspace root; workspace packages must point into the **new** checkout.

Plain Python supports `requirements.txt`, `requirements-dev.txt`, and editable installs for `pyproject.toml` packages / `setup.py` / `setup.cfg`. A `pyproject.toml` containing only lint/test settings does not trigger an install. Conflicting Python manager files require an explicit setup hook or removal of stale files. Repository roots and standard root-level workspaces are autodetected; use a hook for separate projects buried in a larger monorepo or a nonstandard layout.

**Python environments stay in the checkout.** Poetry is forced to create an in-project environment; even its fallback environment metadata is confined to `.worktree-venvs`. uv's project environment is forced to the local `.venv`; Pipenv is forced in-project too. A usable `venv` already present in the target checkout can be used instead. The caller's activated environment, pip target/prefix overrides, and conflicting environment settings do not redirect installs outside the checkout. These safeguards also apply to hook and `exec` environments.

`worktreectl exec NAME -- python ...` finds the checkout's local virtualenv Python and sets `VIRTUAL_ENV` automatically. Autocd alone does not activate a Python environment in your parent shell; use `exec`, your manager's `run` command, or manually source `.venv/bin/activate`.

Python virtualenvs from the **main checkout are never symlinked or copied automatically**: executable shebangs, editable installs, and manager operations can refer back to main or modify it. Each worktree gets an isolated environment while uv/pip/Poetry reuse their normal download caches. pnpm also reuses its normal package store without sharing the checkout's mutable `node_modules` directory.

Copy rules that include an existing virtualenv are rejected for the same reason; remove it from `copy` / `.worktreeinclude` instead of duplicating its nonportable scripts. Automatic installs also refuse existing dependency-directory links and malformed virtualenv directories instead of following or replacing them.

#### Optional `node_modules` sharing

Isolated installs are the default. For a simple project whose installed dependencies you trust, explicitly request sharing:

```sh
worktreectl new experiment --reuse-deps
```

Or set `reuse_dependencies = true` in `[worktreectl]`, or `WORKTREECTL_REUSE_DEPENDENCIES=1`. `--reuse-deps=false` overrides these defaults for a single creation.

Sharing requires identical manifests, lockfiles, relevant manager/runtime config, and a main `node_modules` directory without external/broken links. It is skipped for workspaces, local/file/link dependencies, modern Yarn configuration, or projects without a stable lockfile. If a check fails, a normal isolated install runs instead. Setup hooks remain authoritative and are never bypassed by sharing.

A shared directory is **mutable**, not a copy: do not run installs or edit dependencies through the link. To isolate an explicitly shared checkout before changing dependencies:

```sh
test -L node_modules && unlink node_modules # Removes only the link, not main's files.
npm ci # Or the detected manager's install command.
```

`rm` and `gc` remove the worktree's dependency directories or link along with the checkout; a main-checkout link target is left untouched. Dry runs leave both environments and dependencies unchanged. Generated lockfiles or other uncommitted source changes still require normal commit/stash/force decisions. Existing external virtualenvs made by older releases, custom absolute-path commands, interpreter downloads, and shared package caches are **not** automatically deleted; ownership cannot safely be inferred.

## Cleanup safety

- The main checkout is never stopped or removed, even with `--force`.
- `rm` refuses uncommitted changes and unreferenced detached commits. Unmerged **named** branches are retained; merged branches are deleted only after checking ancestry against the configured cleanup base. Ref deletion compares the expected commit to avoid deleting concurrent branch updates. Git's branch-specific config is left intact.
- `--force` explicitly permits losing uncommitted work, overriding worktree locks, and deleting unmerged branches. `--keep-branch` still wins.
- Process matching uses working directories inside the worktree. Command-line matching is opt-in. The CLI, its ancestors, and its descendants are always protected; same-process-group members are also protected when output feeds a pipe. Removal refuses if those protected group members still depend on the worktree.
- Signal targets are revalidated by PID/start time/command before `SIGTERM`, then escalated to `SIGKILL` after the configured timeout. Process matching is best-effort: subprocesses that change cwd outside the checkout or outlive discovery can be missed. Repositories can have a teardown hook for such services.
- Containers match Compose working-directory labels or bind mounts under the worktree. Compose projects shared across checkouts are refused. Cleanup failures are surfaced, with no broader destructive fallback. Docker checks time out instead of hanging indefinitely.
- Missing process discovery or an unreachable installed Docker daemon blocks cleanup. `--no-docker` is an explicit opt-out and leaves Docker resources untouched. On Linux, inaccessible same-user processes block cleanup unless they belong to the caller's protected process tree; those processes can never be signaled anyway. Other users' inaccessible processes cannot always be inspected; use the same user that started the services.
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

Errors include `error.code` and `error.message`; v0.2.0 adds an optional `error.hint` with a recovery suggestion. Human errors show the same hint on a separate line rather than burying obvious cases in Git's raw command/exit-status output. Partial execution details are included when available, including the kept checkout's path after setup fails. Use `.data.path` for worktree paths, `.data.main_path` for `rm`'s main checkout path, and `.data.exit_code` for `exec` / `new -- CMD` results. Use the JSON fields, not human table formatting, in automation. Completion scripts and shell initialization are raw output and explicitly reject `--json`; help/version support it.

The CLI itself **never prompts**. `--non-interactive` additionally closes stdin to hooks/subprocesses, disallows implicit shells, and disables autocd. Explicit commands normally inherit stdin, so pipes still work.

Exit codes: `0` success, `1` operational failure, `2` invalid CLI usage, `3` safety refusal, `130` interrupted CLI. Subprocess failures propagate their exit code (not executable: `126`; missing executable: `127`; signal termination: `128 + signal`).

## Installation

See the [installation guide](../README.md#install) for the published [installer Gist](https://gist.github.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6) and [GitHub Releases](https://github.com/arno-vel-bept/worktreectl/releases). The installer detects OS/architecture, verifies SHA-256 before extraction, and atomically installs to `~/.local/bin` without sudo. It prints PATH and shell-integration instructions without editing startup files.

`--bin-dir /absolute/path` customizes the destination. `--repo OWNER/REPO` overrides the default `arno-vel-bept/worktreectl` release repository. Environment equivalents: `WORKTREECTL_REPO`, `WORKTREECTL_VERSION`, `WORKTREECTL_BIN_DIR`. Checksums detect corrupted downloads; they do not authenticate a compromised release account or installer source.

### Upgrading

`worktreectl upgrade` (alias `update`) selects the latest stable GitHub release, verifies the archive's SHA-256, and atomically replaces the executable you invoked. It does not read repository configuration, run project hooks, need Go/curl, change directories, or edit shell startup files. Executable symlinks keep pointing to their existing target, which is upgraded in place.

```sh
worktreectl upgrade --check
worktreectl upgrade
worktreectl upgrade --version v0.2.0
worktreectl upgrade --check --json
```

`--check` makes no filesystem changes and does not download an archive. JSON reports `current_version`, `target_version`, `path`, `available`, and `updated`. The default never downgrades; an explicit `--version` deliberately selects that release even if it is older. `--repo` / `WORKTREECTL_REPO` chooses a different trusted release repository, not project TOML settings. HTTPS is required for requests and redirects, and bounded downloads are checksum-verified before extraction. Failures, cancellation, or a concurrent executable replacement preserve the previous/current install.

The install directory must be writable; no sudo is attempted. Upgrade package-manager-owned installations through that manager. Anonymous GitHub API rate limits can affect lookup: retry later or use the installer with an explicit `--version`.

For **v0.1.0**, use the [one-time installer upgrade](../README.md#upgrade); that binary predates the command. `sh install-worktreectl.sh --latest` selects the latest stable release using GitHub's redirect without requiring a JSON parser or API token. `--latest` overrides `WORKTREECTL_VERSION`, but cannot be combined with an explicit `--version`. Reuse your original `--bin-dir` for nondefault installs.

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
