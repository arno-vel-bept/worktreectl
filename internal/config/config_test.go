package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndPrecedence(t *testing.T) {
	main := t.TempDir()
	getenv := func(string) string { return "" }
	s, err := Load(main, "", getenv)
	if err != nil {
		t.Fatal(err)
	}
	if s.BranchPrefix != "wt/" || s.PortBase != 4000 || !s.AutoSetup || !s.AutoCD {
		t.Fatalf("defaults: %+v", s)
	}
	if s.Root != filepath.Join(filepath.Dir(main), filepath.Base(main)+".worktrees") {
		t.Fatal(s.Root)
	}
	data := "[dev]\ntest = ['go', 'test']\n[worktreectl]\nroot = '.trees'\nbranch_prefix = 'task/'\nport_base = 8000\nauto_setup = false\ncopy = []\nsetup = ['make', 'setup']\n"
	if err := os.WriteFile(filepath.Join(main, ".devcli.toml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"WORKTREECTL_PORT_BASE": "9000", "WORKTREECTL_NO_DOCKER": "1"}
	s, err = Load(main, "", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if s.Root != filepath.Join(main, ".trees") || s.BranchPrefix != "task/" || s.PortBase != 9000 || s.AutoSetup || !s.NoDocker || s.Copy == nil {
		t.Fatalf("settings: %+v", s)
	}
}

func TestInvalidSettings(t *testing.T) {
	for _, env := range []map[string]string{
		{"WORKTREECTL_PORT_BASE": "abc"}, {"WORKTREECTL_PORT_BASE": "65535"},
		{"WORKTREECTL_KILL_TIMEOUT": "-1"}, {"WORKTREECTL_MATCH_CMDLINE": "maybe"},
		{"WORKTREECTL_AUTO_CD": "maybe"},
	} {
		if _, err := Load(t.TempDir(), "", func(k string) string { return env[k] }); err == nil {
			t.Fatalf("accepted %v", env)
		}
	}
	if _, err := Load(t.TempDir(), "does-not-exist.toml", func(string) string { return "" }); err == nil {
		t.Fatal("explicit missing config accepted")
	}
}

func TestAutoCDConfigurationAndEnvironment(t *testing.T) {
	main := t.TempDir()
	if err := os.WriteFile(filepath.Join(main, ".devcli.toml"), []byte("[worktreectl]\nauto_cd = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(main, "", func(string) string { return "" })
	if err != nil || s.AutoCD {
		t.Fatalf("config: %+v %v", s, err)
	}
	s, err = Load(main, "", func(k string) string {
		if k == "WORKTREECTL_AUTO_CD" {
			return "1"
		}
		return ""
	})
	if err != nil || !s.AutoCD {
		t.Fatalf("environment override: %+v %v", s, err)
	}
}

func TestUnknownWorktreeSettingFailsButOtherSectionsAreAllowed(t *testing.T) {
	main := t.TempDir()
	path := filepath.Join(main, ".devcli.toml")
	if err := os.WriteFile(path, []byte("[other_tool]\nanything = true\n[worktreectl]\nport_bsae = 4000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(main, "", func(string) string { return "" }); err == nil {
		t.Fatal("unknown setting accepted")
	}
}

func TestDependencyReuseIsOptInAndEnvironmentOverridesConfig(t *testing.T) {
	main := t.TempDir()
	if Defaults().ReuseDeps {
		t.Fatal("dependency sharing must be opt-in")
	}
	if err := os.WriteFile(filepath.Join(main, ".devcli.toml"), []byte("[worktreectl]\nreuse_dependencies = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(main, "", func(string) string { return "" })
	if err != nil || !s.ReuseDeps {
		t.Fatal(s, err)
	}
	s, err = Load(main, "", func(name string) string {
		if name == "WORKTREECTL_REUSE_DEPENDENCIES" {
			return "0"
		}
		return ""
	})
	if err != nil || s.ReuseDeps {
		t.Fatal(s, err)
	}
}
