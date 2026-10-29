#!/bin/sh
# worktreectl bootstrap installer. Binaries live in GitHub Releases, not this Gist.
# Releases: https://github.com/arno-vel-bept/worktreectl/releases
# Installer Gist: https://gist.github.com/arno-vel-bept/50e26e23356eac0536732c416a2aecf6
set -eu

fail() { printf 'worktreectl installer: %s\n' "$*" >&2; exit 1; }
usage() {
    cat <<'EOF'
Usage: sh install.sh --version vX.Y.Z [--bin-dir DIR] [--repo OWNER/REPO]
Installs a checksum-verified release to ~/.local/bin without sudo.
Default release repository: arno-vel-bept/worktreectl.
Environment defaults: WORKTREECTL_REPO, WORKTREECTL_VERSION, WORKTREECTL_BIN_DIR.
EOF
}

repo=${WORKTREECTL_REPO:-arno-vel-bept/worktreectl}
version=${WORKTREECTL_VERSION:-}
bin_dir=${WORKTREECTL_BIN_DIR:-"$HOME/.local/bin"}
while [ "$#" -gt 0 ]; do
    case "$1" in
        --repo) [ "$#" -ge 2 ] || fail '--repo needs a value'; repo=$2; shift 2 ;;
        --version) [ "$#" -ge 2 ] || fail '--version needs a value'; version=$2; shift 2 ;;
        --bin-dir) [ "$#" -ge 2 ] || fail '--bin-dir needs a value'; bin_dir=$2; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) fail "unknown option: $1" ;;
    esac
done

[ -n "$repo" ] || fail 'specify --repo OWNER/REPO (the GitHub release repository)'
# Restrict inputs before constructing URLs or filenames.
printf '%s\n' "$repo" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || fail 'invalid repository; expected OWNER/REPO'
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$' || fail 'specify a versioned release with --version vX.Y.Z'
case "$bin_dir" in /*) ;; *) fail '--bin-dir must be an absolute path' ;; esac

case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail 'unsupported OS; releases support macOS and Linux' ;;
esac
case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) fail 'unsupported architecture; releases support amd64 and arm64' ;;
esac

for tool in curl tar mktemp; do
    command -v "$tool" >/dev/null 2>&1 || fail "required command not found: $tool"
done
if command -v sha256sum >/dev/null 2>&1; then
    checksum_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then
    checksum_tool=shasum
else
    fail 'sha256sum or shasum is required to verify the download'
fi

umask 077
temp=$(mktemp -d "${TMPDIR:-/tmp}/worktreectl-install.XXXXXX") || fail 'cannot create temporary directory'
staged=
cleanup() {
    [ -z "$staged" ] || rm -f "$staged"
    # temp is a private, freshly allocated installer directory, never user input.
    rm -rf "$temp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

asset="worktreectl_${version#v}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"
printf 'Downloading worktreectl %s for %s/%s...\n' "$version" "$os" "$arch" >&2
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 "$base/$asset" -o "$temp/$asset"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 "$base/checksums.txt" -o "$temp/checksums.txt"

expected=$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset {print $1}' "$temp/checksums.txt")
printf '%s\n' "$expected" | grep -Eq '^[a-fA-F0-9]{64}$' || fail "missing or invalid checksum for $asset"
if [ "$checksum_tool" = sha256sum ]; then
    actual=$(sha256sum "$temp/$asset" | awk '{print $1}')
else
    actual=$(shasum -a 256 "$temp/$asset" | awk '{print $1}')
fi
expected=$(printf '%s' "$expected" | tr 'A-F' 'a-f')
[ "$actual" = "$expected" ] || fail 'checksum mismatch; nothing was installed'

# Extract only the binary; never unpack arbitrary archive paths into HOME.
tar -xzf "$temp/$asset" -C "$temp" worktreectl
[ -f "$temp/worktreectl" ] && [ ! -L "$temp/worktreectl" ] || fail 'release archive has no regular worktreectl binary'
mkdir -p "$bin_dir"
[ ! -d "$bin_dir/worktreectl" ] || fail 'install destination is a directory'
# Stage on the destination filesystem and rename atomically (also replaces a
# destination symlink instead of writing through it).
staged=$(mktemp "$bin_dir/.worktreectl.XXXXXX")
cp "$temp/worktreectl" "$staged"
chmod 755 "$staged"
mv -f "$staged" "$bin_dir/worktreectl"
staged=
printf 'Installed %s\n' "$bin_dir/worktreectl" >&2
# Quote apostrophes so printed shell commands are safe to paste.
quoted=$(printf '%s' "$bin_dir" | sed "s/'/'\\\\''/g")
case ":${PATH:-}:" in
    *":$bin_dir:"*) ;;
    *)
        printf "Add this to ~/.zshrc or ~/.bashrc, then open a new shell:\n  export PATH='%s':\"\$PATH\"\n" "$quoted" >&2
        ;;
esac
cat >&2 <<EOF
Enable default autocd by adding this to ~/.zshrc:
  eval "\$('$quoted/worktreectl' shell-init zsh)"
For Bash, use shell-init bash in ~/.bashrc instead.
Disable it with WORKTREECTL_AUTO_CD=0 or --no-cd.
EOF
