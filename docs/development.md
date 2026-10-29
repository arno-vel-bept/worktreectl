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

Linux CI runs Go tests in a private PID namespace and drops back to the normal runner user. This keeps unrelated host services out of process-discovery tests without weakening the CLI's fail-closed behavior or running the tests as root. On a locked-down local Linux machine, unreadable same-user `/proc` entries can legitimately block cleanup; inspect the reported PIDs or use the equivalent isolated test command from [ci.yml](../.github/workflows/ci.yml).

Keep subprocess output off stdout when `--json` is active. Do not introduce prompts into normal commands, silently bypass safety refusals, or shell-evaluate configuration values.

## Code map

| Path | Responsibility |
| --- | --- |
| `cmd/worktreectl` | Entry point, build metadata, and signal cancellation |
| `internal/cli` | Cobra commands, output formatting, JSON envelopes, shell integration |
| `internal/config` | Main-checkout TOML discovery, validation, environment overrides |
| `internal/diagnostic` | User-facing explanations and recovery hints without losing underlying error types |
| `internal/repo` | Git discovery, NUL-delimited worktree records, path/ref resolution |
| `internal/worktree` | Creation, file copying, in-checkout dependency setup, hooks, cleanup orchestration |
| `internal/process` | Working-directory discovery, caller protection, identity-checked signaling |
| `internal/docker` | JSON container discovery, Compose/bind-mount matching, scoped cleanup plans |
| `internal/runner` | Argument-vector subprocess execution and exit-code propagation |
| `internal/update` | HTTPS release lookup, bounded checksum verification, and atomic self-upgrades |
| `tests` | Offline installer fixtures using real archives and SHA-256 checksums |
| `install.sh` | Bootstrap only; users don't need Go or a package manager |

Destructive behavior belongs in Go and must have a read-only dry-run path. Repository-local locks coordinate worktreectl processes, not independent Git clients or service launchers. The [safety reference](reference.md#cleanup-safety) explains the boundary.

## Publish a release

Push a semantic version tag after reviewing the code and checking CI:

```sh
git tag v0.2.1 # Choose a new version after the published v0.2.0 release.
git push origin v0.2.1
```

The [release workflow](../.github/workflows/release.yml) runs GoReleaser and uploads to [arno-vel-bept/worktreectl Releases](https://github.com/arno-vel-bept/worktreectl/releases):

- `worktreectl_<version>_darwin_amd64.tar.gz`
- `worktreectl_<version>_darwin_arm64.tar.gz`
- `worktreectl_<version>_linux_amd64.tar.gz`
- `worktreectl_<version>_linux_arm64.tar.gz`
- `checksums.txt` containing SHA-256 hashes

Binaries embed version, commit, and build time. Release archives must keep a regular `worktreectl` binary at their root—the installer extracts only that file. Local builds and snapshots do not publish releases. Update the README's example version only after the corresponding release assets are published.

An installer **404** means the requested release or asset is unavailable. After pushing the tag, wait for the release workflow to succeed and confirm all five assets above are present on the release page before trying the installer. If the workflow fails, inspect its logs; pushing a tag does not by itself publish binaries. Until a release is available, use the source-build instructions above.

### Local release fallback

If hosted runners are unavailable, validate the reviewed commit locally with `make test` and `make check`, then build the same four archives with GoReleaser v2. The `v0.1.0` release used this fallback after Linux CI passed but GitHub could not allocate a macOS runner.

Start from a clean checkout of the commit to release, and choose a new version. Create the tag **locally**, without pushing it, so the automated release workflow does not race with the manual upload:

```sh
version=v0.2.1 # Choose the next unreleased version.
git tag "$version"
goreleaser release --skip=publish
(cd dist && shasum -a 256 -c checksums.txt)
gh release create "$version" dist/worktreectl_*.tar.gz dist/checksums.txt \
  --repo arno-vel-bept/worktreectl --target "$(git rev-parse HEAD)" \
  --title "worktreectl $version" --generate-notes --draft
```

Do not use `--snapshot`: the installer expects exact versioned filenames. Verify all five uploaded assets on the draft release, then publish:

```sh
gh release edit "$version" --repo arno-vel-bept/worktreectl --draft=false --latest
git fetch origin tag "$version"
```

If an upload fails, inspect the draft and use `gh release upload` for missing assets rather than creating another release. After publishing, test `install.sh --version "$version"` with a temporary `--bin-dir` and confirm the installed binary reports the intended version and commit.

## Maintain the installer Gist

The public installer is hosted on the **same work account**:

- [Gist page](https://gist.github.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6)
- [Pinned raw installer](https://gist.githubusercontent.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6/raw/1d269e77069c049fcd5ce0873efab868bb48ad42/install.sh)
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

The installer requires an explicit version or an explicit `--latest` choice, uses HTTPS for downloads and redirects, checks SHA-256 before extraction, and atomically replaces the destination binary. It does not require sudo, execute the downloaded binary during installation, or edit shell startup files. The self-upgrader uses the same integrity/atomic replacement rules in Go and defaults to the latest stable release. A checksum is integrity checking, not a release-account signature.
