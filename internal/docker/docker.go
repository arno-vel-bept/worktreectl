// Package docker discovers containers via JSON, not human-oriented CLI output.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/arno-vel-bept/worktreectl/internal/repo"
	"github.com/arno-vel-bept/worktreectl/internal/runner"
)

const (
	projectLabel = "com.docker.compose.project"
	workingLabel = "com.docker.compose.project.working_dir"
)

type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Project string `json:"project,omitempty"`
}

type Inspection struct {
	ID     string
	Name   string
	State  struct{ Running bool }
	Config struct{ Labels map[string]string }
	Mounts []struct{ Type, Source string }
}

type Snapshot struct {
	Available  bool
	Disabled   bool
	Warning    string
	Containers []Inspection
}

func Capture(ctx context.Context, disabled bool) (Snapshot, error) {
	s := Snapshot{Disabled: disabled, Containers: []Inspection{}}
	if disabled {
		return s, nil
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return s, nil
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := runner.Output(probe, "", "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		if ctx.Err() != nil {
			return s, ctx.Err()
		}
		s.Warning = "Docker daemon is unreachable; container checks were skipped (use --no-docker to opt out explicitly)"
		return s, nil
	}
	s.Available = true
	query, finish := context.WithTimeout(ctx, 15*time.Second)
	defer finish()
	out, err := runner.Output(query, "", "docker", "ps", "-aq", "--no-trunc")
	if err != nil {
		return s, err
	}
	ids := strings.Fields(string(out))
	// Avoid argv limits on hosts with many containers.
	for i := 0; i < len(ids); i += 100 {
		end := min(i+100, len(ids))
		out, err = runner.Output(query, "", append([]string{"docker", "inspect"}, ids[i:end]...)...)
		if err != nil {
			return s, err
		}
		var batch []Inspection
		if err := json.Unmarshal(out, &batch); err != nil {
			return s, fmt.Errorf("decode docker inspect: %w", err)
		}
		s.Containers = append(s.Containers, batch...)
	}
	return s, nil
}

func (s Snapshot) Match(root string) []Container {
	result := []Container{}
	for _, c := range s.Containers {
		wd := c.Config.Labels[workingLabel]
		matched := wd != "" && repo.Under(wd, root)
		for _, mount := range c.Mounts {
			if mount.Type == "bind" && repo.Under(mount.Source, root) {
				matched = true
			}
		}
		if matched {
			result = append(result, Container{ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"),
				Running: c.State.Running, Project: c.Config.Labels[projectLabel]})
		}
	}
	return result
}

type Action struct {
	Description string   `json:"description"`
	Args        []string `json:"command"`
}

func (s Snapshot) Plan(root string, remove, volumes bool) ([]Action, error) {
	matched := s.Match(root)
	projects := map[string]bool{}
	for _, c := range s.Containers {
		wd, project := c.Config.Labels[workingLabel], c.Config.Labels[projectLabel]
		if project != "" && wd != "" && repo.Under(wd, root) {
			projects[project] = true
		}
	}
	// A shared Compose project name can otherwise tear down another checkout.
	for _, c := range s.Containers {
		if projects[c.Config.Labels[projectLabel]] {
			wd := c.Config.Labels[workingLabel]
			if wd == "" || !repo.Under(wd, root) {
				return nil, fmt.Errorf("Compose project %q is shared with a different checkout; stop it manually", c.Config.Labels[projectLabel])
			}
		}
	}
	result := []Action{}
	names := []string{}
	for project := range projects {
		names = append(names, project)
	}
	sort.Strings(names)
	for _, project := range names {
		args := []string{"docker", "compose", "-p", project}
		// Preserve the project's original config paths, so compose down works
		// when the invocation is not in that checkout.
		for _, c := range s.Containers {
			if c.Config.Labels[projectLabel] == project {
				if wd := c.Config.Labels[workingLabel]; wd != "" {
					args = append(args, "--project-directory", wd)
				}
				for _, file := range strings.Split(c.Config.Labels["com.docker.compose.project.config_files"], ",") {
					if file != "" {
						args = append(args, "-f", file)
					}
				}
				break
			}
		}
		verb := "stop"
		if remove {
			verb = "down"
		}
		args = append(args, verb)
		if remove {
			args = append(args, "--remove-orphans")
			if volumes {
				args = append(args, "--volumes")
			}
		}
		result = append(result, Action{Description: "compose " + project + ": " + verb, Args: args})
	}
	for _, c := range matched {
		if projects[c.Project] || (!remove && !c.Running) {
			continue
		}
		args := []string{"docker", "stop", c.ID}
		if remove {
			args = []string{"docker", "rm", "-f", c.ID}
		}
		result = append(result, Action{Description: "container " + c.Name, Args: args})
	}
	return result, nil
}
