package cli

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/arno-vel-bept/worktreectl/internal/worktree"
)

// A subprocess cannot chdir its parent. This wrapper receives a literal path
// over fd 3, while fd 4 preserves the original stdout (including --json/pipes).
// No temporary files or evaluation of command output are needed.
const shellWrapper = `# worktreectl shell integration; add this initialization to your shell startup file.
worktreectl() {
    local _worktreectl_cd _worktreectl_status
    {
        if _worktreectl_cd="$(command %s --shell-cd "$@" 3>&1 1>&4)"; then
            _worktreectl_status=0
        else
            _worktreectl_status=$?
        fi
    } 4>&1
    if [ -n "$_worktreectl_cd" ]; then
        builtin cd -- "$_worktreectl_cd" || return $?
    fi
    return "$_worktreectl_status"
}
`

func (a *app) shellInitCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "shell-init [bash|zsh]", Short: "Initialize shell autocd integration (eval the output)", Args: exact(1), RunE: func(_ *cobra.Command, args []string) error {
		if a.json {
			return usage("shell-init emits shell code and does not support --json")
		}
		if args[0] != "bash" && args[0] != "zsh" {
			return usage("autocd integration supports bash and zsh, not %q", args[0])
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(a.stdout, shellWrapper, "'"+strings.ReplaceAll(binary, "'", "'\\''")+"'")
		return err
	}}
	cmd.ValidArgs = []string{"bash", "zsh"}
	return cmd
}

func (a *app) requestCD(s *worktree.Service, path string) {
	if s.Config.AutoCD && !a.nonInteractive {
		a.cdTo = path
	}
}

func writeShellCD(path string) error {
	if err := validateShellCD(); err != nil {
		return err
	}
	f := os.NewFile(3, "worktreectl-shell-cd")
	defer f.Close()
	// A trailing slash prevents command substitution from dropping trailing
	// newlines that are part of a valid directory name.
	if _, err := fmt.Fprint(f, path+"/"); err != nil {
		return fmt.Errorf("send autocd directory to shell: %w", err)
	}
	return nil
}

func validateShellCD() error {
	// Do not wrap/close fd 3 before validating it: without a wrapper that fd
	// may belong to the Go runtime rather than our shell protocol.
	var info syscall.Stat_t
	if err := syscall.Fstat(3, &info); err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		return fmt.Errorf("autocd requires shell integration; run eval \"$(worktreectl shell-init bash)\" (or zsh)")
	}
	return nil
}

func removedDirectory(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}
