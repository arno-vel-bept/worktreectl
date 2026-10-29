package process

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMatchProtectsCallerAndPathBoundaries(t *testing.T) {
	s := Snapshot{Processes: []Process{
		{PID: 1},
		{PID: 10, PPID: 1, PGID: 10, Cwd: "/tree"},  // ancestor
		{PID: 20, PPID: 10, PGID: 10, Cwd: "/tree"}, // self
		{PID: 21, PPID: 20, PGID: 10, Cwd: "/tree"}, // descendant
		{PID: 30, PPID: 10, PGID: 30, Cwd: "/tree/sub"},
		{PID: 31, PPID: 10, PGID: 10, Cwd: "/tree"}, // pipe reader
		{PID: 32, PPID: 10, PGID: 32, Cwd: "/tree-other"},
		{PID: 33, PPID: 10, PGID: 33, Command: "vim /tree/file"},
		{PID: 34, PPID: 10, PGID: 34, Command: "vim /tree-other/file"},
		{PID: 35, PPID: 10, PGID: 35, Cwd: "/tree", State: "Z"},
	}}
	matched, skipped := s.Match("/tree", 20, false, true)
	if len(matched) != 1 || matched[0].PID != 30 || len(skipped) != 1 || skipped[0].PID != 31 {
		t.Fatalf("matched=%v skipped=%v", matched, skipped)
	}
	matched, _ = s.Match("/tree", 20, true, false)
	if len(matched) != 3 {
		t.Fatalf("opt-in: %v", matched)
	}
}

func TestParsers(t *testing.T) {
	ps := ParsePS("  42  1 42 S Thu Oct 29 10:00:00 2026 sleep 60\n")
	if len(ps) != 1 || ps[0].Command != "sleep 60" || ps[0].Start != "Thu Oct 29 10:00:00 2026" {
		t.Fatal(ps)
	}
	cwds := ParseCwds("p42\nn/a path\np43\nn/other\n")
	if cwds[42] != "/a path" || cwds[43] != "/other" {
		t.Fatal(cwds)
	}
	cwds = ParseCwds("p42\x00\nn/path\nwith newline\x00\n")
	if cwds[42] != "/path\nwith newline" {
		t.Fatal(cwds)
	}
}

func TestStopRevalidatesIdentity(t *testing.T) {
	c := exec.Command("sleep", "60")
	c.Dir = t.TempDir()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	s, err := Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var target Process
	for _, p := range s.Processes {
		if p.PID == c.Process.Pid {
			target = p
		}
	}
	if target.PID == 0 {
		t.Fatal("spawned process absent from ps")
	}
	fake := target
	fake.Start += " different"
	if err := Stop(context.Background(), []Process{fake}, 0); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(target.PID, 0); err != nil {
		t.Fatal("mismatched PID identity was signaled")
	}
	if err := Stop(context.Background(), []Process{target}, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// A child may still be a zombie until Wait, but it must no longer be alive.
	alive, err := stillSame(context.Background(), target)
	if err != nil || alive {
		t.Fatalf("alive=%v err=%v command=%s", alive, err, strings.TrimSpace(target.Command))
	}
}
