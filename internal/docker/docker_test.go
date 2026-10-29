package docker

import (
	"strings"
	"testing"
)

func inspected(id, project, wd string, running bool, mounts ...string) Inspection {
	c := Inspection{ID: id, Name: "/" + id}
	c.Config.Labels = map[string]string{projectLabel: project, workingLabel: wd, "com.docker.compose.project.config_files": wd + "/compose.yaml"}
	c.State.Running = running
	for _, source := range mounts {
		c.Mounts = append(c.Mounts, struct{ Type, Source string }{"bind", source})
	}
	return c
}

func TestDockerPlans(t *testing.T) {
	s := Snapshot{Available: true, Containers: []Inspection{
		inspected("compose", "repo-feature", "/trees/feature", true),
		inspected("standalone", "", "", true, "/trees/feature/data"),
		inspected("other", "", "", true, "/trees/feature-other/data"),
	}}
	if len(s.Match("/trees/feature")) != 2 {
		t.Fatal(s.Match("/trees/feature"))
	}
	actions, err := s.Plan("/trees/feature", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || !strings.Contains(strings.Join(actions[0].Args, " "), "down --remove-orphans --volumes") || strings.Join(actions[1].Args, " ") != "docker rm -f standalone" {
		t.Fatal(actions)
	}
	s.Containers = append(s.Containers, inspected("collision", "repo-feature", "/another-checkout", true))
	if _, err := s.Plan("/trees/feature", true, false); err == nil {
		t.Fatal("shared project accepted")
	}
}

func TestOnlyBindMountsMatch(t *testing.T) {
	c := inspected("volume", "", "", true, "/trees/feature")
	c.Mounts[0].Type = "volume"
	s := Snapshot{Containers: []Inspection{c}}
	if len(s.Match("/trees/feature")) != 0 {
		t.Fatal("named volume treated as bind mount")
	}
}
