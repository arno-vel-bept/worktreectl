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
assert url.startswith(f'https://github.com/{expected_repo}/releases/download/v1.2.3/')
source = pathlib.Path(os.environ['FIXTURES']) / url.rsplit('/', 1)[-1]
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
