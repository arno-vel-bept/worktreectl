# Development and releases

[Back to the README](../README.md) · [Command reference](reference.md)

## Build and check

Building from source requires Go 1.24+. ShellCheck and Python 3 are needed for the complete check target. GoReleaser v2 is only needed for release archives.

```sh
git clone https://github.com/arno-vel-bept/worktreectl.git
cd worktreectl
make build
./bin/worktreectl --help

# Autocd works from a local build too; initialization binds the binary's absolute path.
eval "$(./bin/worktreectl shell-init zsh)"
```

```sh
make test     # Go tests with race detection; no running Docker daemon required.
make check    # go vet, gofmt check, ShellCheck, offline installer tests.
make snapshot # Four macOS/Linux archives and SHA-256 checksums, without publishing.
```

Format Go before committing:

```sh
gofmt -w cmd internal
```

Keep subprocess output off stdout when `--json` is active. Do not introduce prompts into normal commands, silently bypass safety refusals, or shell-evaluate configuration values.

## Code map

| Path | Responsibility |
| --- | --- |
| `cmd/worktreectl` | Entry point, build metadata, and signal cancellation |
| `internal/cli` | Cobra commands, output formatting, JSON envelopes, shell integration |
| `internal/config` | Main-checkout TOML discovery, validation, environment overrides |
| `internal/repo` | Git discovery, NUL-delimited worktree records, path/ref resolution |
| `internal/worktree` | Creation, file copying, environment generation, hooks, cleanup orchestration |
| `internal/process` | Working-directory discovery, caller protection, identity-checked signaling |
| `internal/docker` | JSON container discovery, Compose/bind-mount matching, scoped cleanup plans |
| `internal/runner` | Argument-vector subprocess execution and exit-code propagation |
| `tests` | Offline installer fixtures using real archives and SHA-256 checksums |
| `install.sh` | Bootstrap only; users don't need Go or a package manager |

Destructive behavior belongs in Go and must have a read-only dry-run path. Repository-local locks coordinate worktreectl processes, not independent Git clients or service launchers. The [safety reference](reference.md#cleanup-safety) explains the boundary.

## Publish a release

Push a semantic version tag after reviewing the code and checking CI:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The [release workflow](../.github/workflows/release.yml) runs GoReleaser and uploads to [arno-vel-bept/worktreectl Releases](https://github.com/arno-vel-bept/worktreectl/releases):

- `worktreectl_<version>_darwin_amd64.tar.gz`
- `worktreectl_<version>_darwin_arm64.tar.gz`
- `worktreectl_<version>_linux_amd64.tar.gz`
- `worktreectl_<version>_linux_arm64.tar.gz`
- `checksums.txt` containing SHA-256 hashes

Binaries embed version, commit, and build time. Release archives must keep a regular `worktreectl` binary at their root—the installer extracts only that file. Local builds and snapshots do not publish releases. After the first release, remove the "First release pending" notice in the README and update the example version when appropriate.

## Maintain the installer Gist

The public installer is hosted on the **same work account**:

- [Gist page](https://gist.github.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6)
- [Pinned raw installer](https://gist.githubusercontent.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6/raw/636ebcb7db3970603183bf5f210191412e8c1cb1/install.sh)
- [Source of truth in this repository](../install.sh)

Keep the Gist and repository copy byte-for-byte identical. Publish changes with GitHub CLI:

```sh
gh auth status # Confirm arno-vel-bept is the active work account.
shellcheck install.sh
python3 -m unittest discover -s tests -p 'test_*.py'
gh gist edit 50e26e23356eac0536732c416a2aecf6 -f install.sh install.sh
gh api gists/50e26e23356eac0536732c416a2aecf6 --jq '.files["install.sh"].raw_url'
```

Review the new raw URL, verify its content, and update pinned links in `README.md` and this guide in the same commit. Gist publication is intentional and manual: the normal repository workflow token is not used to modify a user-owned Gist.

The installer always requests an explicit version, uses HTTPS for downloads and redirects, checks SHA-256 before extraction, and atomically replaces the destination binary. It does not require sudo, execute the downloaded binary during installation, or edit shell startup files. A checksum is integrity checking, not a release-account signature.
