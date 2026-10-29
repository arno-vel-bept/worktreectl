// Package config discovers repository settings without executing configuration.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Settings struct {
	Root         string   `toml:"root"`
	Base         string   `toml:"base"`
	BranchPrefix string   `toml:"branch_prefix"`
	PortBase     int      `toml:"port_base"`
	KillTimeout  int      `toml:"kill_timeout"`
	MatchCmdline bool     `toml:"match_cmdline"`
	NoDocker     bool     `toml:"no_docker"`
	Copy         []string `toml:"copy"`
	Setup        []string `toml:"setup"`
	Teardown     []string `toml:"teardown"`
	AutoSetup    bool     `toml:"auto_setup"`
	AutoCD       bool     `toml:"auto_cd"`
	File         string   `toml:"-"`
}

func Defaults() Settings {
	return Settings{BranchPrefix: "wt/", PortBase: 4000, KillTimeout: 10, AutoSetup: true, AutoCD: true}
}

// Load uses the main checkout's configuration, even when invoked in a linked
// worktree. An explicit path is relative to the invocation directory.
func Load(main, explicit string, getenv func(string) string) (Settings, error) {
	s := Defaults()
	path := explicit
	if path == "" {
		path = filepath.Join(main, ".devcli.toml")
	}
	data, err := os.ReadFile(path)
	if err != nil && (!os.IsNotExist(err) || explicit != "") {
		return s, fmt.Errorf("read configuration: %w", err)
	}
	if err == nil {
		// Other tools can own other sections of .devcli.toml.
		var doc map[string]any
		if err := toml.Unmarshal(data, &doc); err != nil {
			return s, fmt.Errorf("parse %s: %w", path, err)
		}
		if section, exists := doc["worktreectl"]; exists {
			if _, ok := section.(map[string]any); !ok {
				return s, fmt.Errorf("%s: worktreectl must be a TOML table", path)
			}
			encoded, err := toml.Marshal(section)
			if err != nil {
				return s, fmt.Errorf("parse %s: %w", path, err)
			}
			if err := toml.NewDecoder(bytes.NewReader(encoded)).DisallowUnknownFields().Decode(&s); err != nil {
				return s, fmt.Errorf("parse [worktreectl] in %s: %w", path, err)
			}
		}
		s.File, _ = filepath.Abs(path)
	}
	for name, dest := range map[string]*string{
		"ROOT": &s.Root, "BASE": &s.Base, "BRANCH_PREFIX": &s.BranchPrefix,
	} {
		if v := getenv("WORKTREECTL_" + name); v != "" {
			*dest = v
		}
	}
	for name, dest := range map[string]*int{"PORT_BASE": &s.PortBase, "KILL_TIMEOUT": &s.KillTimeout} {
		if v := getenv("WORKTREECTL_" + name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return s, fmt.Errorf("WORKTREECTL_%s must be an integer", name)
			}
			*dest = n
		}
	}
	for name, dest := range map[string]*bool{"MATCH_CMDLINE": &s.MatchCmdline, "NO_DOCKER": &s.NoDocker, "AUTO_CD": &s.AutoCD} {
		if v := getenv("WORKTREECTL_" + name); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return s, fmt.Errorf("WORKTREECTL_%s must be a boolean (0/1 or true/false)", name)
			}
			*dest = b
		}
	}
	if s.PortBase < 1 || s.PortBase > 60535 {
		return s, fmt.Errorf("port_base must be between 1 and 60535")
	}
	if s.KillTimeout < 0 || s.KillTimeout > int((24*time.Hour)/time.Second) {
		return s, fmt.Errorf("kill_timeout must be between 0 and 86400 seconds")
	}
	for name, args := range map[string][]string{"setup": s.Setup, "teardown": s.Teardown} {
		if len(args) > 0 && args[0] == "" {
			return s, fmt.Errorf("%s command must not be empty", name)
		}
	}
	if s.Root == "" {
		s.Root = filepath.Join(filepath.Dir(main), filepath.Base(main)+".worktrees")
	} else if !filepath.IsAbs(s.Root) {
		s.Root = filepath.Join(main, s.Root)
	}
	s.Root = filepath.Clean(s.Root)
	return s, nil
}
