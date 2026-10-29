"""Exercise the bootstrap offline with mocked HTTPS downloads, real archives/checksums."""

import hashlib
import io
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest


INSTALLER = Path(__file__).resolve().parents[1] / "install.sh"


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="worktreectl-installer-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.home = self.root / "home with space"
        self.home.mkdir()
        self.mocks = self.root / "mocks"
        self.mocks.mkdir()
        self.bin = self.home / ".local" / "bin"
        self.asset = "worktreectl_1.2.3_darwin_arm64.tar.gz"
        self.payload = b"#!/bin/sh\nprintf 'fixture binary\\n'\n"
        with tarfile.open(self.root / self.asset, "w:gz") as archive:
            entry = tarfile.TarInfo("worktreectl")
            entry.size = len(self.payload)
            entry.mode = 0o755
            archive.addfile(entry, io.BytesIO(self.payload))
        digest = hashlib.sha256((self.root / self.asset).read_bytes()).hexdigest()
        (self.root / "checksums.txt").write_text(f"{digest}  {self.asset}\n")
        self.mock(
            "uname",
            """#!/bin/sh
case "$1" in
    -s) echo "${TEST_OS:-Darwin}" ;;
    -m) echo "${TEST_ARCH:-arm64}" ;;
esac
""",
        )
        self.mock(
            "curl",
            f"#!{sys.executable}\n" + """
import os, pathlib, shutil, sys
args = sys.argv[1:]
assert '--proto' in args and '=https' in args and '--proto-redir' in args
url = next(a for a in args if a.startswith('https://'))
expected_repo = os.environ.get('EXPECTED_REPO', 'example/worktreectl')
if url == f'https://github.com/{expected_repo}/releases/latest':
    assert '--head' in args and '--write-out' in args
    if os.environ.get('TEST_LATEST_FAIL'):
        sys.exit(22)
    print(os.environ.get('TEST_LATEST_URL', f'https://github.com/{expected_repo}/releases/tag/v1.2.3'), end='')
    sys.exit(0)
assert url.startswith(f'https://github.com/{expected_repo}/releases/download/v1.2.3/')
source = pathlib.Path(os.environ['FIXTURES']) / url.rsplit('/', 1)[-1]
if not source.exists():
    pathlib.Path(args[args.index('-o') + 1]).write_bytes(b'partial download')
    print('curl: The requested URL returned error: 404', file=sys.stderr)
    sys.exit(int(os.environ.get('TEST_CURL_EXIT', '22')))
shutil.copyfile(source, args[args.index('-o') + 1])
""",
        )
        self.env = dict(
            os.environ,
            HOME=str(self.home),
            PATH=f"{self.mocks}{os.pathsep}{os.environ['PATH']}",
            FIXTURES=str(self.root),
            TMPDIR=str(self.root),
        )
        for name in ("WORKTREECTL_REPO", "WORKTREECTL_VERSION", "WORKTREECTL_BIN_DIR"):
            self.env.pop(name, None)

    def mock(self, name, content):
        path = self.mocks / name
        path.write_text(content)
        path.chmod(0o755)

    def run_installer(self, *args, **env):
        return subprocess.run(
            ["sh", str(INSTALLER), *args],
            env=dict(self.env, **env),
            text=True,
            capture_output=True,
            timeout=15,
        )

    def install(self, **env):
        return self.run_installer(
            "--repo", "example/worktreectl", "--version", "v1.2.3", **env
        )

    def test_install_and_path_guidance(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.bin / "worktreectl").read_bytes(), self.payload)
        self.assertEqual((self.bin / "worktreectl").stat().st_mode & 0o777, 0o755)
        self.assertIn("export PATH=", result.stderr)
        self.assertIn("shell-init zsh", result.stderr)
        self.assertIn("WORKTREECTL_AUTO_CD=0", result.stderr)
        self.assertEqual(list(self.root.glob("worktreectl-install.*")), [])

    def test_bad_checksum_preserves_existing_install(self):
        self.bin.mkdir(parents=True)
        (self.bin / "worktreectl").write_text("existing")
        (self.root / "checksums.txt").write_text(f"{'0' * 64}  {self.asset}\n")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum mismatch", result.stderr)
        self.assertEqual((self.bin / "worktreectl").read_text(), "existing")
        self.assertEqual(list(self.root.glob("worktreectl-install.*")), [])

    def test_missing_checksum(self):
        (self.root / "checksums.txt").write_text("unrelated\n")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.bin / "worktreectl").exists())

    def test_missing_release_asset_preserves_existing_install(self):
        (self.root / self.asset).unlink()
        self.bin.mkdir(parents=True)
        (self.bin / "worktreectl").write_text("existing")
        for curl_exit in ("22", "56"):
            with self.subTest(curl_exit=curl_exit):
                result = self.install(TEST_CURL_EXIT=curl_exit)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(
                    "could not download https://github.com/example/worktreectl/"
                    f"releases/download/v1.2.3/{self.asset}",
                    result.stderr,
                )
                self.assertIn(
                    "https://github.com/example/worktreectl/releases/tag/v1.2.3",
                    result.stderr,
                )
                self.assertIn("release workflow must finish", result.stderr)
                self.assertIn("docs/development.md#build-and-check", result.stderr)
                self.assertIn("Nothing was installed", result.stderr)
                self.assertEqual((self.bin / "worktreectl").read_text(), "existing")
                self.assertEqual(list(self.bin.glob(".worktreectl.*")), [])
                self.assertEqual(list(self.root.glob("worktreectl-install.*")), [])

    def test_missing_checksums_download_installs_nothing(self):
        (self.root / "checksums.txt").unlink()
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(
            "could not download https://github.com/example/worktreectl/"
            "releases/download/v1.2.3/checksums.txt",
            result.stderr,
        )
        self.assertIn("Nothing was installed", result.stderr)
        self.assertFalse(self.bin.exists())
        self.assertEqual(list(self.root.glob("worktreectl-install.*")), [])

    def test_replaces_symlink_without_changing_target(self):
        self.bin.mkdir(parents=True)
        outside = self.root / "outside"
        outside.write_text("untouched")
        (self.bin / "worktreectl").symlink_to(outside)
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(outside.read_text(), "untouched")
        self.assertFalse((self.bin / "worktreectl").is_symlink())

    def test_rejects_unsupported_platform_and_missing_version(self):
        for env in ({"TEST_OS": "Windows_NT"}, {"TEST_ARCH": "mips"}):
            result = self.install(**env)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("unsupported", result.stderr)
        result = self.run_installer("--repo", "example/worktreectl")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("versioned release", result.stderr)

    def test_no_path_hint_when_already_present(self):
        result = self.install(PATH=f"{self.bin}:{self.env['PATH']}")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("export PATH=", result.stderr)

    def test_default_public_repository(self):
        result = self.run_installer(
            "--version", "v1.2.3", EXPECTED_REPO="arno-vel-bept/worktreectl"
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.bin / "worktreectl").read_bytes(), self.payload)

    def test_latest_upgrades_existing_install_and_overrides_environment(self):
        self.bin.mkdir(parents=True)
        (self.bin / "worktreectl").write_text("old version")
        result = self.run_installer(
            "--repo", "example/worktreectl", "--latest", WORKTREECTL_VERSION="v0.1.0"
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.bin / "worktreectl").read_bytes(), self.payload)
        self.assertIn("worktreectl upgrade", result.stderr)
        self.assertEqual(list(self.root.glob("worktreectl-install.*")), [])

    def test_latest_rejects_ambiguous_flags_and_invalid_redirects(self):
        result = self.run_installer("--latest", "--version", "v1.2.3")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("mutually exclusive", result.stderr)
        for url in (
            "https://github.com/example/worktreectl/releases/tag/not-a-version",
            "https://github.com/untrusted/other/releases/tag/v1.2.3",
        ):
            result = self.run_installer(
                "--repo", "example/worktreectl", "--latest", TEST_LATEST_URL=url
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(self.bin.exists())

    def test_latest_lookup_failure_preserves_existing_install(self):
        self.bin.mkdir(parents=True)
        (self.bin / "worktreectl").write_text("old version")
        result = self.run_installer(
            "--repo", "example/worktreectl", "--latest", TEST_LATEST_FAIL="1"
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("latest published release", result.stderr)
        self.assertEqual((self.bin / "worktreectl").read_text(), "old version")
        self.assertEqual(list(self.root.glob("worktreectl-install.*")), [])

    def test_latest_accepts_github_canonical_repository_case(self):
        result = self.run_installer(
            "--repo", "Example/WorktreeCtl", "--latest",
            EXPECTED_REPO="Example/WorktreeCtl",
            TEST_LATEST_URL="https://github.com/example/worktreectl/releases/tag/v1.2.3",
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_linux_and_architecture_aliases(self):
        asset = "worktreectl_1.2.3_linux_amd64.tar.gz"
        data = (self.root / self.asset).read_bytes()
        (self.root / asset).write_bytes(data)
        digest = hashlib.sha256(data).hexdigest()
        (self.root / "checksums.txt").write_text(f"{digest}  {asset}\n")
        result = self.install(TEST_OS="Linux", TEST_ARCH="x86_64")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.bin / "worktreectl").read_bytes(), self.payload)

    def test_path_guidance_quotes_apostrophes(self):
        custom = self.home / "it's bin"
        result = self.run_installer(
            "--repo", "example/worktreectl", "--version", "v1.2.3",
            "--bin-dir", str(custom),
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        directive = next(
            line.strip()
            for line in result.stderr.splitlines()
            if line.strip().startswith("export PATH=")
        )
        check = subprocess.run(
            ["sh", "-c", directive + '\nprintf "%s" "$PATH"'],
            env=self.env,
            text=True,
            capture_output=True,
        )
        self.assertEqual(check.returncode, 0, check.stderr)
        self.assertTrue(check.stdout.startswith(str(custom) + ":"), check.stdout)


if __name__ == "__main__":
    unittest.main()
