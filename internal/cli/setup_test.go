package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestPythonEnvironmentsStayLocalAndAreFullyRemoved(t *testing.T) {
	for _, manager := range []string{"poetry", "uv"} {
		t.Run(manager, func(t *testing.T) {
			main, root := fixture(t)
			project := "[tool.poetry]\npackage-mode = false\n"
			if manager == "uv" {
				project = "[tool.uv]\n"
			}
			write(t, filepath.Join(main, "pyproject.toml"), project)
			git(t, main, "add", "pyproject.toml")
			git(t, main, "commit", "-m", "python fixture")
			parentVenv := filepath.Join(main, ".venv")
			writeExecutable(t, filepath.Join(parentVenv, "bin", "python"), "#!/bin/sh\nprintf 'wrong parent interpreter'\n")
			write(t, filepath.Join(parentVenv, "pyvenv.cfg"), "main environment\n")
			mocks := t.TempDir()
			writeExecutable(t, filepath.Join(mocks, manager), `#!/bin/sh
[ "$POETRY_VIRTUALENVS_IN_PROJECT" = true ] || exit 90
[ "$POETRY_VIRTUALENVS_CREATE" = true ] || exit 91
[ "$UV_PROJECT_ENVIRONMENT" = "$WORKTREE_PATH/.venv" ] || exit 92
[ "$POETRY_VIRTUALENVS_PATH" = "$WORKTREE_PATH/.worktree-venvs" ] || exit 93
[ -z "${VIRTUAL_ENV:-}" ] || exit 94
/bin/mkdir -p .venv/bin "$POETRY_VIRTUALENVS_PATH"
printf '#!/bin/sh\nprintf "%%s\\n" "$VIRTUAL_ENV"\n' > .venv/bin/python
/bin/chmod 755 .venv/bin/python
printf 'worktree environment\n' > .venv/pyvenv.cfg
printf 'local cache metadata\n' > "$POETRY_VIRTUALENVS_PATH/fixture"
`)
			t.Setenv("PATH", filepath.Join(parentVenv, "bin")+string(os.PathListSeparator)+mocks+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("VIRTUAL_ENV", parentVenv)
			t.Setenv("POETRY_VIRTUALENVS_IN_PROJECT", "false")
			t.Setenv("POETRY_VIRTUALENVS_PATH", t.TempDir())
			t.Setenv("UV_PROJECT_ENVIRONMENT", parentVenv)
			run(t, 0, "new", "python", "--json")
			path := filepath.Join(root, "python")
			if info, err := os.Lstat(filepath.Join(path, ".venv")); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				t.Fatal("worktree environment was not local", info, err)
			}
			out, _ := run(t, 0, "exec", "python", "--", "python")
			if strings.TrimSpace(out) != filepath.Join(path, ".venv") {
				t.Fatal("exec did not select local Python:", out)
			}
			if status := git(t, path, "status", "--porcelain"); status != "" {
				t.Fatal("dependency artifacts prevent safe cleanup:", status)
			}
			run(t, 0, "rm", "python", "--dry-run")
			if _, err := os.Stat(filepath.Join(path, ".venv")); err != nil {
				t.Fatal("dry run removed environment", err)
			}
			run(t, 0, "rm", "python")
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("worktree environment/cache was not removed", err)
			}
			data, err := os.ReadFile(filepath.Join(parentVenv, "pyvenv.cfg"))
			if err != nil || string(data) != "main environment\n" {
				t.Fatal("main environment was changed", string(data), err)
			}
		})
	}
}

func TestNodeDependenciesAreIsolatedByDefaultAndSharedOnlyOnRequest(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "isolated", true: "reuse"}[reuse], func(t *testing.T) {
			main, root := fixture(t)
			write(t, filepath.Join(main, "package.json"), `{"name":"fixture","private":true}`)
			write(t, filepath.Join(main, "package-lock.json"), "{}")
			git(t, main, "add", "package.json", "package-lock.json")
			git(t, main, "commit", "-m", "node fixture")
			if err := os.MkdirAll(filepath.Join(main, "node_modules", "fixture"), 0755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(main, "node_modules", "fixture", "index.js"), "main dependencies")
			mocks := t.TempDir()
			writeExecutable(t, filepath.Join(mocks, "npm"), "#!/bin/sh\n/bin/mkdir -p node_modules/fixture\nprintf 'local dependencies' > node_modules/fixture/index.js\n")
			t.Setenv("PATH", mocks+string(os.PathListSeparator)+os.Getenv("PATH"))
			args := []string{"new", "node", "--json"}
			if reuse {
				args = append(args, "--reuse-deps")
			}
			run(t, 0, args...)
			path := filepath.Join(root, "node")
			info, err := os.Lstat(filepath.Join(path, "node_modules"))
			if err != nil || (info.Mode()&os.ModeSymlink != 0) != reuse {
				t.Fatal("wrong dependency isolation", info, err)
			}
			run(t, 0, "rm", "node")
			data, err := os.ReadFile(filepath.Join(main, "node_modules", "fixture", "index.js"))
			if err != nil || string(data) != "main dependencies" {
				t.Fatal("cleanup/install touched main dependencies", string(data), err)
			}
		})
	}
}
