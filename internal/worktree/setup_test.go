package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arno-vel-bept/worktreectl/internal/config"
	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
	"github.com/arno-vel-bept/worktreectl/internal/repo"
	"github.com/arno-vel-bept/worktreectl/internal/runner"
)

func setupWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func setupTools(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		file := filepath.Join(dir, name)
		setupWrite(t, file, "#!/bin/sh\nexit 0\n")
		if err := os.Chmod(file, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

func setupVenv(t *testing.T, path string) {
	t.Helper()
	python := filepath.Join(path, "bin", "python")
	setupWrite(t, python, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(python, 0755); err != nil {
		t.Fatal(err)
	}
	setupWrite(t, filepath.Join(path, "pyvenv.cfg"), "fixture")
}

func TestNodeSetupChoosesDeclaredManagerAndLocks(t *testing.T) {
	cases := []struct {
		name, manifest string
		files          map[string]string
		want           []string
	}{
		{"npm", `{}`, nil, []string{"npm", "install"}},
		{"npm lock", `{}`, map[string]string{"package-lock.json": "{}"}, []string{"npm", "ci"}},
		{"npm shrinkwrap", `{}`, map[string]string{"npm-shrinkwrap.json": "{}"}, []string{"npm", "ci"}},
		{"pnpm lock", `{}`, map[string]string{"pnpm-lock.yaml": "lockfileVersion: '9.0'"}, []string{"pnpm", "install", "--frozen-lockfile"}},
		{"declared pnpm", `{"packageManager":"pnpm@10.0.0"}`, nil, []string{"pnpm", "install"}},
		{"declaration wins", `{"packageManager":"npm@10.0.0"}`, map[string]string{"pnpm-lock.yaml": "", "package-lock.json": "{}"}, []string{"npm", "ci"}},
		{"yarn classic", `{}`, map[string]string{"yarn.lock": "# yarn lockfile v1"}, []string{"yarn", "install", "--frozen-lockfile"}},
		{"yarn modern", `{"packageManager":"yarn@4.0.0"}`, map[string]string{"yarn.lock": "__metadata:"}, []string{"yarn", "install", "--immutable"}},
		{"bun lock", `{}`, map[string]string{"bun.lock": "{}"}, []string{"bun", "install", "--frozen-lockfile"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			setupWrite(t, filepath.Join(path, "package.json"), tc.manifest)
			for file, data := range tc.files {
				setupWrite(t, filepath.Join(path, file), data)
			}
			got, err := nodeSetup(path)
			if err != nil || !reflect.DeepEqual(got, [][]string{tc.want}) {
				t.Fatalf("got %v (%v), want %v", got, err, tc.want)
			}
		})
	}
}

func TestNodeSetupRejectsAmbiguousAndInvalidManifests(t *testing.T) {
	for _, manifest := range []string{`{"packageManager":"unknown@1"}`, `{invalid`, `{}`, `null`} {
		path := t.TempDir()
		setupWrite(t, filepath.Join(path, "package.json"), manifest)
		setupWrite(t, filepath.Join(path, "pnpm-lock.yaml"), "")
		setupWrite(t, filepath.Join(path, "package-lock.json"), "{}")
		if _, err := nodeSetup(path); err == nil || diagnostic.Hint(err) == "" {
			t.Fatalf("accepted %s or no hint: %v", manifest, err)
		}
	}
}

func TestPythonManagerDetection(t *testing.T) {
	setupTools(t, "uv", "poetry", "pipenv", "python3")
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"uv lock", map[string]string{"uv.lock": ""}, []string{"uv", "sync", "--locked"}},
		{"uv metadata", map[string]string{"pyproject.toml": "[tool.uv]\n"}, []string{"uv", "sync"}},
		{"uv backend", map[string]string{"pyproject.toml": "[build-system]\nbuild-backend = 'uv_build'\n"}, []string{"uv", "sync"}},
		{"poetry lock", map[string]string{"poetry.lock": ""}, []string{"poetry", "install", "--no-interaction"}},
		{"poetry metadata", map[string]string{"pyproject.toml": "[tool.poetry]\npackage-mode = false\n"}, []string{"poetry", "install", "--no-interaction"}},
		{"poetry pep621", map[string]string{"pyproject.toml": "[build-system]\nbuild-backend = 'poetry.core.masonry.api'\n"}, []string{"poetry", "install", "--no-interaction"}},
		{"pipenv", map[string]string{"Pipfile": ""}, []string{"pipenv", "install", "--dev"}},
		{"pipenv lock", map[string]string{"Pipfile.lock": "{}"}, []string{"pipenv", "sync", "--dev"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			for file, data := range tc.files {
				setupWrite(t, filepath.Join(path, file), data)
			}
			got, err := pythonSetup(path)
			if err != nil || !reflect.DeepEqual(got, [][]string{tc.want}) {
				t.Fatalf("got %v (%v), want %v", got, err, tc.want)
			}
		})
	}
}

func TestPipSetupUsesLocalInterpreterAndEditableProject(t *testing.T) {
	setupTools(t, "python3")
	path := t.TempDir()
	setupWrite(t, filepath.Join(path, "requirements.txt"), "")
	setupWrite(t, filepath.Join(path, "requirements-dev.txt"), "")
	setupWrite(t, filepath.Join(path, "pyproject.toml"), "[project]\nname = 'fixture'\n")
	got, err := pythonSetup(path)
	want := [][]string{
		{"python3", "-m", "venv", ".venv"},
		{filepath.Join(path, ".venv", "bin", "python"), "-m", "pip", "install", "-r", "requirements.txt", "-r", "requirements-dev.txt", "-e", "."},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v (%v), want %v", got, err, want)
	}
	setupVenv(t, filepath.Join(path, ".venv"))
	got, err = pythonSetup(path)
	if err != nil || !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("did not reuse existing LOCAL venv: %v (%v)", got, err)
	}
}

func TestPythonDoesNotTreatLintOnlyPyprojectAsPackage(t *testing.T) {
	path := t.TempDir()
	setupWrite(t, filepath.Join(path, "pyproject.toml"), "[tool.ruff]\nline-length = 100\n")
	got, err := pythonSetup(path)
	if err != nil || len(got) != 0 {
		t.Fatalf("unexpected install: %v (%v)", got, err)
	}
}

func TestMissingManagerDoesNotFallBackToPipOrNpm(t *testing.T) {
	setupTools(t, "python3", "npm")
	path := t.TempDir()
	setupWrite(t, filepath.Join(path, "poetry.lock"), "")
	_, err := pythonSetup(path)
	var exit *runner.ExitError
	if !errors.As(err, &exit) || exit.Code != 127 || !strings.Contains(err.Error(), "poetry") || diagnostic.Hint(err) == "" {
		t.Fatal(err)
	}
	if _, err := requireTool("pnpm", "pnpm-lock.yaml"); err == nil {
		t.Fatal("fell back to npm")
	}
}

func TestCorepackFallback(t *testing.T) {
	setupTools(t, "corepack")
	got, err := requireTool("pnpm", "pnpm-lock.yaml")
	if err != nil || !reflect.DeepEqual(got, []string{"corepack", "pnpm"}) {
		t.Fatal(got, err)
	}
}

func TestEnvironmentIsolatesPythonAndActivatesLocalVenv(t *testing.T) {
	path, foreign := t.TempDir(), t.TempDir()
	setupVenv(t, filepath.Join(path, ".venv"))
	t.Setenv("VIRTUAL_ENV", foreign)
	t.Setenv("PATH", filepath.Join(foreign, "bin")+string(os.PathListSeparator)+"/usr/bin")
	t.Setenv("POETRY_VIRTUALENVS_IN_PROJECT", "false")
	t.Setenv("POETRY_VIRTUALENVS_CREATE", "false")
	t.Setenv("POETRY_VIRTUALENVS_PATH", foreign)
	t.Setenv("UV_PROJECT_ENVIRONMENT", foreign)
	t.Setenv("PIP_TARGET", foreign)
	setupWrite(t, filepath.Join(path, ".worktree.env"), "VIRTUAL_ENV='"+foreign+"'\nUV_PROJECT_ENVIRONMENT='"+foreign+"'\n")
	env, err := environment(path)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	for key, want := range map[string]string{
		"VIRTUAL_ENV": filepath.Join(path, ".venv"), "UV_PROJECT_ENVIRONMENT": filepath.Join(path, ".venv"),
		"POETRY_VIRTUALENVS_IN_PROJECT": "true", "POETRY_VIRTUALENVS_CREATE": "true", "POETRY_VIRTUALENVS_PATH": filepath.Join(path, ".worktree-venvs"),
		"PIPENV_VENV_IN_PROJECT": "1", "PIPENV_IGNORE_VIRTUALENVS": "0", "PIP_TARGET": "",
	} {
		if values[key] != want {
			t.Errorf("%s = %q, want %q", key, values[key], want)
		}
	}
	if strings.Contains(values["PATH"], foreign) || !strings.HasPrefix(values["PATH"], filepath.Join(path, ".venv", "bin")) {
		t.Fatal(values["PATH"])
	}
}

func TestPythonRefusesExternalAndBrokenVenv(t *testing.T) {
	setupTools(t, "uv")
	path, foreign := t.TempDir(), t.TempDir()
	setupWrite(t, filepath.Join(path, "uv.lock"), "")
	setupWrite(t, filepath.Join(foreign, "marker"), "keep")
	if err := os.Symlink(foreign, filepath.Join(path, ".venv")); err != nil {
		t.Fatal(err)
	}
	if _, err := pythonSetup(path); err == nil {
		t.Fatal("accepted external venv")
	}
	if _, err := environment(path); err == nil {
		t.Fatal("hook could use external venv")
	}
	data, err := os.ReadFile(filepath.Join(foreign, "marker"))
	if err != nil || string(data) != "keep" {
		t.Fatal("external environment was touched")
	}
	if err := os.Remove(filepath.Join(path, ".venv")); err != nil {
		t.Fatal(err)
	}
	setupWrite(t, filepath.Join(path, ".venv", "user-file"), "keep")
	if _, err := pythonSetup(path); err == nil || !strings.Contains(err.Error(), "not a usable") {
		t.Fatal("accepted a malformed environment directory", err)
	}
}

func TestNodeReuseGuardsAndNeverRemovesMainDependencies(t *testing.T) {
	for _, mode := range []string{"matching", "different lock", "workspace", "local dependency", "external link", "broken link", "no lock"} {
		t.Run(mode, func(t *testing.T) {
			main, path, foreign := t.TempDir(), t.TempDir(), t.TempDir()
			manifest := `{"dependencies":{"fixture":"1.0.0"}}`
			if mode == "workspace" {
				manifest = `{"workspaces":["packages/*"]}`
			}
			if mode == "local dependency" {
				manifest = `{"dependencies":{"fixture":"file:../fixture"}}`
			}
			for _, dir := range []string{main, path} {
				setupWrite(t, filepath.Join(dir, "package.json"), manifest)
				if mode != "no lock" {
					setupWrite(t, filepath.Join(dir, "package-lock.json"), "{}")
				}
			}
			if mode == "different lock" {
				setupWrite(t, filepath.Join(path, "package-lock.json"), "{\"changed\":true}")
			}
			setupWrite(t, filepath.Join(main, "node_modules", "fixture", "index.js"), "keep")
			if mode == "external link" || mode == "broken link" {
				target := foreign
				if mode == "broken link" {
					target = filepath.Join(foreign, "missing")
				}
				if err := os.Symlink(target, filepath.Join(main, "node_modules", "linked")); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			s := &Service{Repo: &repo.Repo{Main: main}, Config: config.Defaults(), Stderr: io.Discard, Log: func(format string, args ...any) { fmt.Fprintf(&logs, format, args...) }}
			got, err := s.reuseNodeModules(path)
			if err != nil || got != (mode == "matching") {
				t.Fatalf("reused=%v err=%v logs=%s", got, err, &logs)
			}
			if got {
				if err := os.Remove(filepath.Join(path, "node_modules")); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(filepath.Join(main, "node_modules", "fixture", "index.js"))
			if err != nil || string(data) != "keep" {
				t.Fatal("main dependencies were modified")
			}
		})
	}
}

func TestAutoSetupRefreshesEnvironmentAfterVenvCreation(t *testing.T) {
	tools := setupTools(t, "python3")
	spy := filepath.Join(tools, "python3")
	setupWrite(t, spy, `#!/bin/sh
if [ "$1" = -m ] && [ "$2" = venv ]; then
    /bin/mkdir -p "$3/bin"
    /bin/cp "$0" "$3/bin/python"
    printf 'fixture\n' > "$3/pyvenv.cfg"
else
    printf '%s\n' "$VIRTUAL_ENV" > "$TEST_RESULT"
fi
`)
	if err := os.Chmod(spy, 0755); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	setupWrite(t, filepath.Join(path, "requirements.txt"), "")
	result := filepath.Join(t.TempDir(), "result")
	t.Setenv("TEST_RESULT", result)
	s := &Service{Repo: &repo.Repo{Main: t.TempDir()}, Config: config.Defaults(), Stderr: io.Discard, Log: func(string, ...any) {}}
	if err := s.autoSetup(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result)
	if err != nil || strings.TrimSpace(string(data)) != filepath.Join(path, ".venv") {
		t.Fatalf("pip did not run in the new environment: %s (%v)", data, err)
	}
}
