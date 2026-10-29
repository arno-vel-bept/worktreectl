package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
	"github.com/arno-vel-bept/worktreectl/internal/repo"
	"github.com/arno-vel-bept/worktreectl/internal/runner"
	"github.com/pelletier/go-toml/v2"
)

func exists(path, name string) bool {
	_, err := os.Lstat(filepath.Join(path, name))
	return err == nil
}

func validVenv(path string) bool {
	info, err := os.Stat(filepath.Join(path, "bin", "python"))
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 && exists(path, "pyvenv.cfg")
}

func pythonEnvPath(path string) string {
	if !exists(path, ".venv") && validVenv(filepath.Join(path, "venv")) {
		return filepath.Join(path, "venv")
	}
	return filepath.Join(path, ".venv")
}

// Check the directory itself, not bin/python: venv interpreters legitimately
// link to the system Python. Never install through a dependency-directory link.
func localDependency(path, name string) error {
	full := filepath.Join(path, name)
	info, err := os.Lstat(full)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", full, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !repo.Under(full, path) {
		return refuseHint(fmt.Sprintf("cannot install dependencies into %s: it is not a local directory", full), "Remove or rename the dependency-directory link/file yourself, then retry setup. Its target was not changed.")
	}
	return nil
}

func requireTool(tool, marker string) ([]string, error) {
	if _, err := exec.LookPath(tool); err == nil {
		return []string{tool}, nil
	}
	if tool == "pnpm" || tool == "yarn" {
		if _, err := exec.LookPath("corepack"); err == nil {
			return []string{"corepack", tool}, nil
		}
	}
	hint := fmt.Sprintf("Install %s and add it to PATH, then retry setup; use --no-setup to create a checkout without installing dependencies.", tool)
	return nil, diagnostic.New(fmt.Sprintf("%s requires %s, but that command is not available", marker, tool), hint, &runner.ExitError{Code: 127, Err: fmt.Errorf("missing %s", tool)})
}

func (s *Service) autoSetup(ctx context.Context, path string) error {
	js, err := nodeSetup(path)
	if err != nil {
		return err
	}
	python, err := pythonSetup(path)
	if err != nil {
		return err
	}
	commands := [][]string{}
	if len(js) > 0 {
		if err := localDependency(path, "node_modules"); err != nil {
			return err
		}
		reused := false
		if s.Config.ReuseDeps && !exists(path, "node_modules") {
			reused, err = s.reuseNodeModules(path)
			if err != nil {
				return err
			}
		}
		if !reused && !exists(path, "node_modules") {
			tool, err := requireTool(js[0][0], "JavaScript dependency setup")
			if err != nil {
				return err
			}
			commands = append(commands, append(tool, js[0][1:]...))
		}
	}
	commands = append(commands, python...)
	if exists(path, "go.mod") {
		tool, err := requireTool("go", "go.mod")
		if err != nil {
			return err
		}
		commands = append(commands, append(tool, "mod", "download"))
	}
	for _, args := range commands {
		// Refresh after venv creation, so every later step uses the local Python.
		env, err := environment(path)
		if err != nil {
			return err
		}
		s.Log("setup: %s", strings.Join(args, " "))
		if err := runner.Run(ctx, path, env, s.Stdin, s.Stderr, s.Stderr, args...); err != nil {
			return err
		}
	}
	return nil
}

func nodeSetup(path string) ([][]string, error) {
	data, err := os.ReadFile(filepath.Join(path, "package.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read package.json: %w", err)
	}
	var manifest struct {
		PackageManager string `json:"packageManager"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, diagnostic.New("package.json is not a valid package manifest", "Use a JSON object with a string packageManager field (when present).", err)
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, diagnostic.New("package.json must contain a JSON object", "Fix package.json before installing dependencies.", nil)
	}
	manager, version, _ := strings.Cut(manifest.PackageManager, "@")
	if manager == "" {
		locks := []struct{ file, manager string }{
			{"pnpm-lock.yaml", "pnpm"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"},
			{"yarn.lock", "yarn"}, {"package-lock.json", "npm"}, {"npm-shrinkwrap.json", "npm"},
		}
		for _, lock := range locks {
			if !exists(path, lock.file) {
				continue
			}
			if manager != "" && manager != lock.manager {
				return nil, diagnostic.New("multiple JavaScript package managers have lockfiles in this checkout", "Set packageManager in package.json to select the intended manager, or remove stale lockfiles.", nil)
			}
			manager = lock.manager
		}
		if manager == "" {
			manager = "npm"
		}
	}
	args := []string{"install"}
	switch manager {
	case "npm":
		if exists(path, "package-lock.json") || exists(path, "npm-shrinkwrap.json") {
			args = []string{"ci"}
		}
	case "pnpm":
		if exists(path, "pnpm-lock.yaml") {
			args = append(args, "--frozen-lockfile")
		}
	case "bun":
		if exists(path, "bun.lock") || exists(path, "bun.lockb") {
			args = append(args, "--frozen-lockfile")
		}
	case "yarn":
		if exists(path, "yarn.lock") {
			lock, err := os.ReadFile(filepath.Join(path, "yarn.lock"))
			if err != nil {
				return nil, err
			}
			if (version != "" && !strings.HasPrefix(version, "1.")) || exists(path, ".yarnrc.yml") || bytes.Contains(lock, []byte("__metadata:")) {
				args = append(args, "--immutable")
			} else {
				args = append(args, "--frozen-lockfile")
			}
		}
	default:
		return nil, diagnostic.New(fmt.Sprintf("unsupported JavaScript package manager %q", manager), "Configure a setup hook for this manager, or use npm, pnpm, Yarn, or Bun.", nil)
	}
	return [][]string{append([]string{manager}, args...)}, nil
}

func pythonSetup(path string) ([][]string, error) {
	var project map[string]any
	data, err := os.ReadFile(filepath.Join(path, "pyproject.toml"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read pyproject.toml: %w", err)
	}
	if err == nil {
		if err := toml.Unmarshal(data, &project); err != nil {
			return nil, diagnostic.New("pyproject.toml is not valid TOML", "Fix pyproject.toml before installing dependencies.", err)
		}
	}
	tools, _ := project["tool"].(map[string]any)
	_, uv := tools["uv"]
	_, poetry := tools["poetry"]
	uv = uv || exists(path, "uv.lock")
	poetry = poetry || exists(path, "poetry.lock")
	buildSystem, _ := project["build-system"].(map[string]any)
	backend, _ := buildSystem["build-backend"].(string)
	poetry = poetry || strings.HasPrefix(backend, "poetry.core.")
	uv = uv || backend == "uv_build"
	pipenv := exists(path, "Pipfile") || exists(path, "Pipfile.lock")
	_, pepProject := project["project"]
	_, build := project["build-system"]
	packaged := pepProject || build || exists(path, "setup.py") || exists(path, "setup.cfg")
	requirements := []string{}
	for _, file := range []string{"requirements.txt", "requirements-dev.txt"} {
		if exists(path, file) {
			requirements = append(requirements, "-r", file)
		}
	}
	if !uv && !poetry && !pipenv && !packaged && len(requirements) == 0 {
		return nil, nil // A pyproject containing only lint/test settings is not a package.
	}
	if (uv && poetry) || (pipenv && (uv || poetry)) {
		return nil, diagnostic.New("multiple Python environment managers are configured in this checkout", "Remove stale manager files, or configure an explicit setup hook in .devcli.toml.", nil)
	}
	venv := pythonEnvPath(path)
	name, _ := filepath.Rel(path, venv)
	if err := localDependency(path, name); err != nil {
		return nil, err
	}
	if exists(path, name) && !validVenv(venv) {
		return nil, diagnostic.New(fmt.Sprintf("%s exists but is not a usable Python virtualenv", venv), "Rename it or repair it before retrying setup; existing files were left untouched.", nil)
	}
	if uv || poetry || pipenv {
		manager, marker, args := "uv", "uv.lock or [tool.uv]", []string{"sync"}
		if uv && exists(path, "uv.lock") {
			args = append(args, "--locked")
		}
		if poetry {
			manager, marker, args = "poetry", "poetry.lock or [tool.poetry]", []string{"install", "--no-interaction"}
		}
		if pipenv {
			manager, marker, args = "pipenv", "Pipfile", []string{"install", "--dev"}
			if exists(path, "Pipfile.lock") {
				args = []string{"sync", "--dev"}
			}
		}
		tool, err := requireTool(manager, marker)
		if err != nil {
			return nil, err
		}
		return [][]string{append(tool, args...)}, nil
	}
	commands := [][]string{}
	if !validVenv(venv) {
		tool, err := requireTool("python3", "Python dependency setup")
		if err != nil {
			return nil, err
		}
		commands = append(commands, append(tool, "-m", "venv", name))
	}
	args := append([]string{filepath.Join(venv, "bin", "python"), "-m", "pip", "install"}, requirements...)
	if packaged {
		args = append(args, "-e", ".")
	}
	return append(commands, args), nil
}

// Sharing is opt-in: even matching dependencies can be mutated by later manual
// installs. Workspace/local packages must resolve to the NEW checkout instead.
func (s *Service) reuseNodeModules(path string) (bool, error) {
	source := filepath.Join(s.Repo.Main, "node_modules")
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		s.Log("dependency reuse skipped: main node_modules is not a local directory")
		return false, nil
	}
	files := []string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", ".npmrc", ".yarnrc", ".yarnrc.yml", ".pnpmfile.cjs", "bunfig.toml", ".nvmrc", ".node-version", ".tool-versions", "mise.toml", ".mise.toml"}
	hasLock := false
	for _, file := range files {
		a, ae := os.ReadFile(filepath.Join(s.Repo.Main, file))
		b, be := os.ReadFile(filepath.Join(path, file))
		if os.IsNotExist(ae) && os.IsNotExist(be) {
			continue
		}
		if ae != nil || be != nil || !bytes.Equal(a, b) {
			s.Log("dependency reuse skipped: %s differs from the main checkout", file)
			return false, nil
		}
		if strings.Contains(file, "lock") || file == "npm-shrinkwrap.json" {
			hasLock = true
		}
	}
	data, err := os.ReadFile(filepath.Join(path, "package.json"))
	if err != nil {
		return false, err
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, err
	}
	_, workspaces := manifest["workspaces"]
	if !hasLock || workspaces || exists(path, "pnpm-workspace.yaml") || exists(path, ".yarnrc.yml") || localPackage(manifest) {
		s.Log("dependency reuse skipped: workspace/local dependencies or no stable lockfile")
		return false, nil
	}
	unsafeLink := false
	err = filepath.WalkDir(source, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(file)
			if err != nil || !repo.Under(target, source) {
				unsafeLink = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("inspect main node_modules: %w", err)
	}
	if unsafeLink {
		s.Log("dependency reuse skipped: node_modules contains external or broken links")
		return false, nil
	}
	if err := os.Symlink(source, filepath.Join(path, "node_modules")); err != nil {
		return false, err
	}
	s.Log("reusing node_modules from %s (no install was run)", s.Repo.Main)
	s.Log("warning: node_modules is shared; unlink it before changing dependencies so the main checkout stays untouched")
	return true, nil
}

func localPackage(value any) bool {
	switch v := value.(type) {
	case string:
		for _, prefix := range []string{"file:", "link:", "workspace:", "portal:", "./", "../"} {
			if strings.HasPrefix(v, prefix) {
				return true
			}
		}
	case map[string]any:
		for _, child := range v {
			if localPackage(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if localPackage(child) {
				return true
			}
		}
	}
	return false
}
