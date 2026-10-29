// Package process finds worktree processes and protects the invoking process
// tree. Discovery is read-only; signal targets are revalidated before use.
package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arno-vel-bept/worktreectl/internal/repo"
	"github.com/arno-vel-bept/worktreectl/internal/runner"
)

type Process struct {
	PID     int      `json:"pid"`
	PPID    int      `json:"-"`
	PGID    int      `json:"-"`
	Start   string   `json:"-"`
	State   string   `json:"-"`
	Command string   `json:"command"`
	Cwd     string   `json:"cwd"`
	Ports   []string `json:"ports,omitempty"`
}

type Snapshot struct {
	Processes []Process
	Reliable  bool
	Warning   string
}

func Capture(ctx context.Context) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := runner.Output(ctx, "", "ps", "-A", "-ww", "-o", "pid=,ppid=,pgid=,stat=,lstart=,command=")
	if err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{Processes: ParsePS(string(out))}
	foundSelf := false
	for _, p := range s.Processes {
		if p.PID == os.Getpid() {
			foundSelf = true
			break
		}
	}
	if !foundSelf {
		return Snapshot{}, fmt.Errorf("process snapshot did not include the caller; cannot establish protected processes")
	}
	cwds := map[int]string{}
	switch runtime.GOOS {
	case "linux":
		s.Reliable = true
		protected, _ := s.protected(os.Getpid())
		unreadable := []int{}
		for _, p := range s.Processes {
			dir := filepath.Join("/proc", strconv.Itoa(p.PID))
			info, err := os.Stat(dir)
			if err != nil {
				continue
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Getuid()) {
				continue
			}
			cwd, err := os.Readlink(filepath.Join(dir, "cwd"))
			if err == nil {
				cwds[p.PID] = cwd
			} else if os.IsPermission(err) && !protected[p.PID] {
				// Protected caller processes can never be cleanup targets, so their
				// inaccessible cwd does not hide a process we would signal. Other
				// unreadable same-user processes still fail closed.
				unreadable = append(unreadable, p.PID)
			}
		}
		if cwds[os.Getpid()] == "" {
			s.Reliable = false
			s.Warning = "cannot inspect the caller's working directory; check /proc permissions"
		} else if len(unreadable) > 0 {
			s.Reliable = false
			s.Warning = fmt.Sprintf("working directories for process(es) %v are inaccessible; check /proc permissions", unreadable)
		}
	case "darwin":
		// lsof normally exits 1 when other users' processes cannot be inspected.
		// A successful cwd result for this process verifies that it is usable.
		out, _ := runner.Output(ctx, "", "lsof", "-n", "-P", "-w", "-d", "cwd", "-F0pn")
		cwds = ParseCwds(string(out))
		s.Reliable = cwds[os.Getpid()] != ""
		if !s.Reliable {
			s.Warning = "process discovery unavailable; install/allow lsof to inspect working directories"
		}
	default:
		s.Warning = "process discovery supports macOS and Linux only"
	}
	if ctx.Err() != nil {
		return Snapshot{}, ctx.Err()
	}
	for i := range s.Processes {
		s.Processes[i].Cwd = cwds[s.Processes[i].PID]
	}
	return s, nil
}

func ParsePS(out string) []Process {
	result := []Process{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		ppid, e2 := strconv.Atoi(fields[1])
		pgid, e3 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		result = append(result, Process{PID: pid, PPID: ppid, PGID: pgid,
			State: fields[3], Start: strings.Join(fields[4:9], " "), Command: strings.Join(fields[9:], " ")})
	}
	return result
}

func ParseCwds(out string) map[int]string {
	result := map[int]string{}
	pid := 0
	separator := "\n"
	if strings.Contains(out, "\x00") {
		separator = "\x00"
	}
	for _, line := range strings.Split(out, separator) {
		line = strings.TrimLeft(line, "\n") // lsof inserts newlines between record sets.
		if strings.HasPrefix(line, "p") {
			pid, _ = strconv.Atoi(line[1:])
		} else if strings.HasPrefix(line, "n") && pid > 0 {
			result[pid] = line[1:]
		}
	}
	return result
}

func (s Snapshot) Match(root string, self int, byCommand, piped bool) (matched, skipped []Process) {
	matched, skipped = []Process{}, []Process{}
	protected, byPID := s.protected(self)
	for _, p := range s.Processes {
		if protected[p.PID] || strings.HasPrefix(p.State, "Z") {
			continue
		}
		if (p.Cwd != "" && repo.Under(p.Cwd, root)) || (byCommand && mentions(p.Command, root)) {
			if piped && byPID[self].PGID != 0 && p.PGID == byPID[self].PGID {
				skipped = append(skipped, p)
			} else {
				matched = append(matched, p)
			}
		}
	}
	return matched, skipped
}

// Use the same caller-protection graph for discovery and signal matching.
func (s Snapshot) protected(self int) (map[int]bool, map[int]Process) {
	byPID := map[int]Process{}
	for _, p := range s.Processes {
		byPID[p.PID] = p
	}
	protected := map[int]bool{0: true, 1: true}
	seenAncestors := map[int]bool{}
	for q := self; q > 0 && !seenAncestors[q]; q = byPID[q].PPID {
		seenAncestors[q] = true
		protected[q] = true
	}
	for _, p := range s.Processes {
		if protected[p.PID] {
			continue
		}
		seen := map[int]bool{}
		for q := p.PPID; q > 0 && !seen[q]; q = byPID[q].PPID {
			seen[q] = true
			if q == self {
				protected[p.PID] = true
				break
			}
		}
	}
	return protected, byPID
}

func mentions(command, root string) bool {
	for _, path := range []string{filepath.Clean(root), repo.Physical(root)} {
		for rest := command; ; {
			i := strings.Index(rest, path)
			if i < 0 {
				break
			}
			before, after := rest[:i], rest[i+len(path):]
			left := len(before) == 0 || strings.ContainsRune(" \t='\"", rune(before[len(before)-1]))
			right := len(after) == 0 || strings.ContainsRune("/ \t'\"", rune(after[0]))
			if left && right {
				return true
			}
			rest = after
		}
	}
	return false
}

func Piped(w *os.File) bool {
	info, err := w.Stat()
	return err == nil && info.Mode()&os.ModeNamedPipe != 0
}

func Ports(ctx context.Context, pid int) []string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, _ := runner.Output(ctx, "", "lsof", "-n", "-P", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-sTCP:LISTEN", "-Fn")
	ports := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") && !seen[line[1:]] {
			ports = append(ports, line[1:])
			seen[line[1:]] = true
		}
	}
	sort.Strings(ports)
	return ports
}

func stillSame(ctx context.Context, target Process) (bool, error) {
	out, err := runner.Output(ctx, "", "ps", "-ww", "-p", strconv.Itoa(target.PID), "-o", "pid=,ppid=,pgid=,stat=,lstart=,command=")
	if err != nil {
		if syscall.Kill(target.PID, 0) == syscall.ESRCH {
			return false, nil
		}
		return false, err
	}
	for _, p := range ParsePS(string(out)) {
		if p.PID == target.PID {
			return p.Start == target.Start && p.Command == target.Command && !strings.HasPrefix(p.State, "Z"), nil
		}
	}
	return false, nil
}

func Stop(ctx context.Context, targets []Process, timeout time.Duration) error {
	signal := func(p Process, sig syscall.Signal) (bool, error) {
		alive, err := stillSame(ctx, p)
		if !alive || err != nil {
			return false, err
		}
		if err := syscall.Kill(p.PID, sig); err != nil && err != syscall.ESRCH {
			return false, fmt.Errorf("signal process %d: %w", p.PID, err)
		}
		return true, nil
	}
	for _, p := range targets {
		if _, err := signal(p, syscall.SIGTERM); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(timeout)
	escalated := false
	for {
		remaining := []Process{}
		for _, p := range targets {
			alive, err := stillSame(ctx, p)
			if err != nil {
				return err
			}
			if alive {
				remaining = append(remaining, p)
			}
		}
		if len(remaining) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			if escalated {
				return fmt.Errorf("processes are still running after SIGKILL; cleanup aborted")
			}
			for _, p := range remaining {
				if _, err := signal(p, syscall.SIGKILL); err != nil {
					return err
				}
			}
			escalated = true
			deadline = time.Now().Add(3 * time.Second)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		targets = remaining
	}
}
