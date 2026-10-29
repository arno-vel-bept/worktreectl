# worktreectl

**Branch out. Work in parallel. Clean up with confidence.**

[![CI](https://github.com/arno-vel-bept/worktreectl/actions/workflows/ci.yml/badge.svg)](https://github.com/arno-vel-bept/worktreectl/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux-7AA2F7)](https://github.com/arno-vel-bept/worktreectl/releases)

One checkout for your feature. Another for an experiment. A third for an agent. **worktreectl** keeps each Git worktree's local files, setup, ports, processes, and containers together—and helps you clean them up without guessing what is safe to delete.

Built in Go with [Cobra](https://github.com/spf13/cobra) and [Lip Gloss](https://github.com/charmbracelet/lipgloss). Polished on a terminal; predictable in a pipeline. No TUI to navigate, no surprise prompts.

**[Install](#install) · [Upgrade](#upgrade) · [Quick start](#quick-start) · [Configuration](#one-binary-every-repository) · [Full reference](docs/reference.md) · [Development](docs/development.md)**

## Why worktreectl?

- **Start in one command.** Create a branch and checkout, copy your local `.env` files, run setup, and enter the new directory.
- **Keep parallel work isolated.** Each worktree gets its own environment, advisory port assignment, and Compose project name.
- **Know what is still running.** Inspect processes, listening TCP ports, and associated containers before cleanup.
- **Preview before deleting.** Dry runs, main-checkout protection, dirty-worktree refusals, and detached-commit checks come first.
- **Bring your own workflow.** Repository-local TOML settings and executable hooks work across languages and projects.
- **Give CI real data.** JSON on stdout, diagnostics on stderr, meaningful exit codes, and explicit noninteractive execution.

## Install

**No Go, package manager, or sudo needed to install a released binary.** Supported targets: macOS and Linux, on amd64 and arm64. Git 2.31+ is required; macOS process discovery uses `lsof`. Docker/Compose v2 are optional.

The [public installer Gist](https://gist.github.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6) downloads binaries from [GitHub Releases](https://github.com/arno-vel-bept/worktreectl/releases), verifies their SHA-256 checksums, and installs to `~/.local/bin`.

Current release: **[v0.2.0](https://github.com/arno-vel-bept/worktreectl/releases/tag/v0.2.0)**.

### Download and inspect first (recommended)

```sh
curl --fail --location --proto '=https' --proto-redir '=https' \
  https://gist.githubusercontent.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6/raw/1d269e77069c049fcd5ce0873efab868bb48ad42/install.sh \
  -o install-worktreectl.sh
less install-worktreectl.sh
sh install-worktreectl.sh --version v0.2.0
```

The raw URL is pinned to the reviewed installer revision, not a moving Gist URL.

### Prefer the shortcut?

Use only after trusting the installer source:

```sh
curl --fail --location --proto '=https' --proto-redir '=https' \
  https://gist.githubusercontent.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6/raw/1d269e77069c049fcd5ce0873efab868bb48ad42/install.sh | \
  sh -s -- --version v0.2.0
```

The installer prints any PATH changes you need; it never edits shell startup files. Customize the destination with `--bin-dir /absolute/path`. Checksums catch corrupted downloads, not a compromised release account.

## Upgrade

Starting with **v0.2.0**, upgrade in place with no installer, Go, or curl required:

```sh
worktreectl upgrade --check # Optional: show the latest stable version without changing files.
worktreectl upgrade         # Verify checksums and atomically replace this executable.
```

This works outside Git repositories and keeps your install path, project files, and shell integration unchanged. Failed downloads leave the previous binary intact. Use `worktreectl upgrade --version vX.Y.Z` to choose an exact published release; the default never downgrades a newer installed version.

**Already on v0.1.0?** That binary has no upgrade command. Do this once, after reviewing/trusting the installer:

```sh
curl --fail --location --proto '=https' --proto-redir '=https' \
  https://gist.githubusercontent.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6/raw/1d269e77069c049fcd5ce0873efab868bb48ad42/install.sh \
  -o install-worktreectl.sh
sh install-worktreectl.sh --latest
worktreectl version
```

If you originally used a custom `--bin-dir`, pass that same directory again. The installer can always be rerun with `--latest` or `--version` to upgrade/reinstall any previous version. Package-manager-owned or read-only installations should use their package manager, or be reinstalled into a user-owned directory such as `~/.local/bin`.

## Quick start

Run from an existing Git repository with at least one commit:

```sh
# Enable autocd. Add this to ~/.zshrc; Bash users use shell-init bash in ~/.bashrc.
eval "$(worktreectl shell-init zsh)"

worktreectl new feature --no-setup # Create it and enter it. Omit --no-setup to run setup.
worktreectl exec -- npm run dev   # Run your command with the worktree environment.
worktreectl list                  # See branches, changes, ahead counts, and processes.
worktreectl ps                    # Inspect this checkout's processes and containers.
worktreectl rm --dry-run          # Preview the cleanup. Nothing is stopped or deleted.
worktreectl rm                    # Clean up and return to the main checkout.
```

Autocd needs the shell function because a binary cannot change its parent shell's directory. Disable it with `--no-cd`, `WORKTREECTL_AUTO_CD=0`, or TOML `auto_cd = false`. Direct binary calls still print paths for manual `cd` and scripts. [How shell integration works →](docs/reference.md#automatic-directory-changes)

### The commands you'll actually use

| Command | What it does |
| --- | --- |
| `new [NAME] [-- CMD...]` | Create a worktree and branch; copy local files; run setup |
| `exec [NAME] [-- CMD...]` | Run a command with that worktree's environment |
| `path [NAME]` | Print its path |
| `list` / `ps [NAME]` | Inspect checkouts and the resources tied to them |
| `stop [NAME]` | Stop resources but keep the checkout |
| `rm [NAME]` | Teardown, stop resources, and remove the checkout |
| `gc` | Remove only clean, merged, idle, unlocked worktrees |
| `upgrade [--check]` | Upgrade this binary to the latest stable release, independently of repositories |
| `completion SHELL` / `shell-init SHELL` | Set up completions and shell autocd |

Names can be a directory name, full path, or branch. Omit the name to target the current checkout. Use `--` before subprocess flags, and `worktreectl COMMAND --help` for every option.

## One binary. Every repository.

Place a `.devcli.toml` in the **main checkout**. It is discovered even from linked worktrees:

```toml
[worktreectl]
root = "../my-project.worktrees"
base = "main"
branch_prefix = "wt/"
auto_cd = true
copy = [".env", ".env.*"]
setup = ["make", "setup"]
teardown = ["make", "teardown"]
```

Commands are argv arrays, not implicitly evaluated shell expressions. Without configured hooks, executable `.worktreectl/setup` and `.worktreectl/teardown` files are supported; setup can also autodetect common Node, Python, and Go dependency managers.

Everything is optional. See the [annotated example](examples/devcli.toml) and [configuration reference](docs/reference.md#repository-configuration) for defaults, environment overrides, local-file copying, and hook behavior.

### What's new in v0.2.0

- **Helpful errors.** Common mistakes such as running outside a Git repository get a short explanation and a recovery hint, including in JSON.
- **Local Python environments.** uv, Poetry, Pipenv, and plain pip setup keep environments in the checkout so removal cleans them up. `exec -- python` selects the local environment.
- **Manager-aware JS/TS setup.** Respect `packageManager`, npm/pnpm workspace installs, and frozen lockfiles. Isolated dependencies by default; guarded `node_modules` sharing is opt-in.
- **Easy upgrades.** Run `worktreectl upgrade` for the latest stable release, or `upgrade --check` to check without changing files.

See the [dependency setup guide](docs/reference.md#dependency-setup) for supported layouts, cleanup ownership, and sharing limitations.

## Safe defaults—not silent destruction

The main checkout is never removed. Dirty worktrees and unreferenced detached commits block ordinary removal. Unmerged named branches are retained. Missing process discovery, an unreachable installed Docker daemon, and shared Compose projects block cleanup rather than hiding uncertainty.

`--force` deliberately overrides protections for uncommitted work, locks, and unmerged branches. `--volumes` deliberately removes Compose volumes. **Use `--dry-run` first.** Matching running resources is best-effort; hooks execute trusted repository code. [Read the safety rules and limits →](docs/reference.md#cleanup-safety)

## Friendly to agents and CI

```sh
worktreectl new ci-check --no-setup --non-interactive --json
worktreectl exec ci-check --non-interactive --json -- go test ./...
worktreectl rm ci-check --non-interactive --json
```

`--json` emits exactly one result envelope on stdout, including errors. Hook/subprocess output goes to stderr. `--non-interactive` closes subprocess stdin and disables autocd; the CLI itself never prompts. [Output schema and exit codes →](docs/reference.md#automation-and-output)

## Built to be small—and easy to understand

Go for the application. Cobra for commands and completions. Lip Gloss for terminal polish. GoReleaser for four-platform archives and checksums. Shell is limited to the installer and the small autocd wrapper.

Tests use disposable repositories, real Bash/Zsh integration, and offline installer fixtures. The original [Bash prototype](worktreectl.sh) is kept as a reference; the Go CLI does not call it.

Want to contribute or cut a release? Start with the [development guide](docs/development.md).
