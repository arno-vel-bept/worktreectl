// Package cli provides Cobra commands and the machine-readable output contract.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/arno-vel-bept/worktreectl/internal/config"
	"github.com/arno-vel-bept/worktreectl/internal/process"
	"github.com/arno-vel-bept/worktreectl/internal/repo"
	"github.com/arno-vel-bept/worktreectl/internal/runner"
	"github.com/arno-vel-bept/worktreectl/internal/worktree"
)

type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type usageError struct{ err error }

func (e *usageError) Error() string          { return e.err.Error() }
func (e *usageError) Unwrap() error          { return e.err }
func usage(format string, args ...any) error { return &usageError{fmt.Errorf(format, args...)} }

type envelope struct {
	Command  string     `json:"command"`
	Data     any        `json:"data,omitempty"`
	Error    *jsonError `json:"error,omitempty"`
	Warnings []string   `json:"warnings"`
}

type jsonError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type app struct {
	stdin                          io.Reader
	stdout, stderr                 io.Writer
	build                          BuildInfo
	json, nonInteractive, noDocker bool
	noDockerSet                    bool
	noCD, noCDSet, shellCD         bool
	cdTo                           string
	color, configPath              string
	warnings                       []string
	data                           any
	command                        string
	service                        *worktree.Service
}

func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, build BuildInfo) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr, build: build, warnings: []string{}, command: "worktreectl"}
	root := a.root()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if a.shellCD && a.cdTo != "" {
		if cdErr := writeShellCD(a.cdTo); cdErr != nil {
			if err == nil {
				err = cdErr
			} else {
				a.log("warning: %s", cdErr)
			}
		}
	}
	code := 0
	if err != nil {
		code = 1
		var u *usageError
		var refusal *worktree.Refusal
		var child *runner.ExitError
		switch {
		case ctx.Err() != nil:
			code = 130
		case errors.As(err, &u):
			code = 2
		case errors.As(err, &refusal):
			code = 3
		case errors.As(err, &child):
			code = child.Code
		}
		if a.json || wantsJSON(args) {
			err = json.NewEncoder(stdout).Encode(envelope{Command: a.command, Data: a.data, Error: &jsonError{Code: code, Message: err.Error()}, Warnings: a.warnings})
			if err != nil {
				fmt.Fprintf(stderr, "worktreectl: write JSON: %v\n", err)
				return 1
			}
		} else {
			fmt.Fprintf(stderr, "%s %v\n", a.style(stderr, "#F7768E").Render("worktreectl: error:"), err)
		}
	} else if a.json {
		if err := json.NewEncoder(stdout).Encode(envelope{Command: a.command, Data: a.data, Warnings: a.warnings}); err != nil {
			fmt.Fprintf(stderr, "worktreectl: write JSON: %v\n", err)
			return 1
		}
	}
	return code
}

func wantsJSON(args []string) bool {
	on := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--json" || arg == "--json=true" {
			on = true
		}
		if arg == "--json=false" {
			on = false
		}
	}
	return on
}

func (a *app) root() *cobra.Command {
	showVersion := false
	root := &cobra.Command{
		Use: "worktreectl", Short: "Create, inspect, and safely clean up Git worktrees",
		Long:          "worktreectl manages parallel Git checkouts, local setup, processes, and containers.\nNever prompts. Data goes to stdout; diagnostics go to stderr.",
		SilenceErrors: true, SilenceUsage: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usage("unknown command %q (try: worktreectl help)", args[0])
			}
			return nil
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			a.command = cmd.Name()
			a.noDockerSet = cmd.Flags().Changed("no-docker")
			a.noCDSet = cmd.Flags().Changed("no-cd")
			if a.shellCD {
				if err := validateShellCD(); err != nil {
					return usage("%s", err)
				}
			}
			if a.color != "auto" && a.color != "always" && a.color != "never" {
				return usage("--color must be auto, always, or never")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if showVersion {
				return a.printVersion()
			}
			return cmd.Help()
		},
	}
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &usageError{err} })
	root.Flags().BoolVarP(&showVersion, "version", "V", false, "Print build information")
	f := root.PersistentFlags()
	f.BoolVar(&a.json, "json", false, "Emit one JSON result; subprocess output goes to stderr")
	f.BoolVar(&a.nonInteractive, "non-interactive", false, "Disable subprocess stdin and require an explicit command for exec")
	f.StringVar(&a.color, "color", "auto", "Color output: auto, always, never (auto respects NO_COLOR)")
	f.StringVar(&a.configPath, "config", "", "Read settings from this TOML file instead of the main checkout's .devcli.toml")
	f.BoolVar(&a.noDocker, "no-docker", false, "Explicitly skip all Docker discovery and cleanup")
	f.BoolVar(&a.noCD, "no-cd", false, "Disable shell autocd after new/rm for this invocation")
	f.BoolVar(&a.shellCD, "shell-cd", false, "Internal shell-integration protocol")
	_ = f.MarkHidden("shell-cd")
	root.AddCommand(a.shellInitCommand())
	root.AddCommand(a.newCommand(), a.execCommand(), a.pathCommand(), a.listCommand(), a.psCommand(), a.stopCommand(), a.removeCommand(), a.gcCommand())
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print build information", Args: exact(0), RunE: func(_ *cobra.Command, _ []string) error {
		return a.printVersion()
	}})
	completion := &cobra.Command{Use: "completion [bash|zsh|fish|powershell]", Short: "Generate a shell completion script", Args: exact(1), RunE: func(cmd *cobra.Command, args []string) error {
		if a.json {
			return usage("completion emits a shell script and does not support --json")
		}
		switch args[0] {
		case "bash":
			return root.GenBashCompletionV2(cmd.OutOrStdout(), true)
		case "zsh":
			return root.GenZshCompletion(cmd.OutOrStdout())
		case "fish":
			return root.GenFishCompletion(cmd.OutOrStdout(), true)
		case "powershell":
			return root.GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
		default:
			return usage("unknown shell %q", args[0])
		}
	}}
	completion.ValidArgs = []string{"bash", "zsh", "fish", "powershell"}
	root.AddCommand(completion)
	root.CompletionOptions.DisableDefaultCmd = true
	_ = root.MarkPersistentFlagFilename("config", "toml")
	for _, cmd := range root.Commands() {
		switch cmd.Name() {
		case "exec", "path", "ps", "stop", "rm":
			cmd.ValidArgsFunction = a.completeTargets
		case "new":
			for _, flag := range []string{"base", "branch"} {
				_ = cmd.RegisterFlagCompletionFunc(flag, a.completeRefs)
			}
		}
	}
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !a.json {
			defaultHelp(cmd, args)
			return
		}
		var buffer strings.Builder
		previous := cmd.OutOrStdout()
		cmd.SetOut(&buffer)
		defaultHelp(cmd, args)
		cmd.SetOut(previous)
		a.data = map[string]string{"help": buffer.String()}
	})
	return root
}

func (a *app) completeTargets(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 || cmd.ArgsLenAtDash() >= 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	s, err := a.init(cmd.Context())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	trees, err := s.Repo.Trees(cmd.Context())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	result, seen := []string{}, map[string]bool{}
	for _, w := range trees {
		for _, value := range []string{w.Name, w.Branch, w.Path} {
			if value != "" && strings.HasPrefix(value, prefix) && !seen[value] {
				result = append(result, value)
				seen[value] = true
			}
		}
	}
	return result, cobra.ShellCompDirectiveNoFileComp
}

func (a *app) completeRefs(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	s, err := a.init(cmd.Context())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out, err := s.Repo.Git(cmd.Context(), "for-each-ref", "--format=%(refname:short)", "refs/heads/", "refs/remotes/", "refs/tags/")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	result := []string{}
	for _, ref := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ref != "" && strings.HasPrefix(ref, prefix) {
			result = append(result, ref)
		}
	}
	return result, cobra.ShellCompDirectiveNoFileComp
}

func (a *app) printVersion() error {
	a.data = a.build
	if !a.json {
		_, err := fmt.Fprintf(a.stdout, "worktreectl %s (commit %s, built %s)\n", a.build.Version, a.build.Commit, a.build.Date)
		return err
	}
	return nil
}

func exact(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return &usageError{err}
		}
		return nil
	}
}

func atMost(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.MaximumNArgs(n)(cmd, args); err != nil {
			return &usageError{err}
		}
		return nil
	}
}

func (a *app) init(ctx context.Context) (*worktree.Service, error) {
	if a.service != nil {
		return a.service, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	r, err := repo.Discover(ctx, cwd)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(r.Main, a.configPath, os.Getenv)
	if err != nil {
		return nil, err
	}
	cfg.Root = repo.Physical(cfg.Root)
	if cfg.Base != "" {
		if strings.HasPrefix(cfg.Base, "-") {
			return nil, fmt.Errorf("invalid configured base %q", cfg.Base)
		}
		if _, err := r.Git(ctx, "rev-parse", "--verify", "--end-of-options", cfg.Base+"^{commit}"); err != nil {
			return nil, err
		}
		r.Base = cfg.Base
	}
	if a.noDockerSet {
		cfg.NoDocker = a.noDocker
	}
	if a.noCDSet {
		cfg.AutoCD = !a.noCD
	}
	piped := false
	for _, w := range []io.Writer{a.stdout, a.stderr} {
		if f, ok := w.(*os.File); ok && process.Piped(f) {
			piped = true
		}
	}
	stdin := a.stdin
	if a.nonInteractive {
		stdin = nil
	}
	a.service = &worktree.Service{Repo: r, Config: cfg, Stdin: stdin, Stdout: a.stdout, Stderr: a.stderr, Piped: piped, Log: a.log}
	return a.service, nil
}

func (a *app) log(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if strings.HasPrefix(message, "warning: ") {
		a.warnings = append(a.warnings, strings.TrimPrefix(message, "warning: "))
	}
	fmt.Fprintf(a.stderr, "%s %s\n", a.style(a.stderr, "#7AA2F7").Render("worktreectl:"), message)
}

func terminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *app) colorful(w io.Writer) bool {
	return a.color == "always" || (a.color == "auto" && os.Getenv("NO_COLOR") == "" && terminal(w))
}

func (a *app) style(w io.Writer, color string) lipgloss.Style {
	r := lipgloss.NewRenderer(w)
	if !a.colorful(w) {
		r.SetColorProfile(termenv.Ascii)
	} else {
		r.SetColorProfile(termenv.TrueColor)
	}
	return r.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
}

func (a *app) renderTable(headers []string, rows [][]string) error {
	if a.colorful(a.stdout) {
		t := table.New().Headers(headers...).Rows(rows...).Border(lipgloss.RoundedBorder()).
			StyleFunc(func(row, _ int) lipgloss.Style {
				if row == table.HeaderRow {
					return a.style(a.stdout, "#7AA2F7").Padding(0, 1)
				}
				return a.style(a.stdout, "#C0CAF5").Bold(false).Padding(0, 1)
			})
		_, err := fmt.Fprintln(a.stdout, t.Render())
		return err
	}
	w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(w, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return w.Flush()
}

func target(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// Cobra preserves the position of --. It prevents command arguments/flags from
// accidentally being interpreted as worktreectl arguments.
func splitCommand(cmd *cobra.Command, args []string) (string, []string, error) {
	n := cmd.ArgsLenAtDash()
	if n < 0 {
		n = len(args)
	}
	if n > 1 {
		return "", nil, usage("expected at most one worktree name before --; put command arguments after --")
	}
	name := ""
	if n == 1 {
		name = args[0]
	}
	return name, args[n:], nil
}

func (a *app) newCommand() *cobra.Command {
	o := worktree.NewOptions{}
	c := &cobra.Command{Use: "new [NAME] [flags] [-- CMD...]", Aliases: []string{"start"}, Short: "Create a worktree, copy local files, and run setup", RunE: func(cmd *cobra.Command, args []string) error {
		name, child, err := splitCommand(cmd, args)
		if err != nil {
			return err
		}
		o.Name = name
		if o.Detach && o.Branch != "" {
			return usage("--detach and --branch are mutually exclusive")
		}
		s, err := a.init(cmd.Context())
		if err != nil {
			return err
		}
		unlock, err := s.Lock(cmd.Context())
		if err != nil {
			return err
		}
		result, err := func() (worktree.NewResult, error) { defer unlock(); return s.New(cmd.Context(), o) }()
		if err != nil {
			return err
		}
		a.data = result
		if len(child) > 0 {
			if err := a.runChild(cmd.Context(), s, result.Path, child); err != nil {
				return err
			}
			a.requestCD(s, result.Path)
			return nil
		}
		if !a.json {
			_, err = fmt.Fprintln(a.stdout, result.Path)
		}
		if err == nil {
			a.requestCD(s, result.Path)
		}
		return err
	}}
	f := c.Flags()
	f.StringVarP(&o.Base, "base", "b", "", "Start from this ref (default: current checkout's HEAD)")
	f.StringVar(&o.Branch, "branch", "", "Branch to create/reuse (default: configured prefix + NAME)")
	f.BoolVarP(&o.Detach, "detach", "d", false, "Create a detached HEAD instead of a branch")
	f.BoolVar(&o.Fetch, "fetch", false, "Fetch/prune remotes before creation")
	f.BoolVar(&o.NoCopy, "no-copy", false, "Skip copying local files")
	f.BoolVar(&o.NoSetup, "no-setup", false, "Skip setup hooks and dependency installation")
	return c
}

func (a *app) runChild(ctx context.Context, s *worktree.Service, path string, args []string) error {
	err := s.Exec(ctx, path, args, a.json)
	code := 0
	if err != nil {
		code = 1
		var child *runner.ExitError
		if errors.As(err, &child) {
			code = child.Code
		}
	}
	a.data = struct {
		Path     string `json:"path"`
		ExitCode int    `json:"exit_code"`
	}{path, code}
	return err
}

func (a *app) execCommand() *cobra.Command {
	return &cobra.Command{Use: "exec [NAME] [-- CMD...]", Aliases: []string{"enter"}, Short: "Run a command (or $SHELL) with the worktree environment", RunE: func(cmd *cobra.Command, args []string) error {
		name, child, err := splitCommand(cmd, args)
		if err != nil {
			return err
		}
		if len(child) == 0 && (a.nonInteractive || !readerTerminal(a.stdin)) {
			return usage("exec without CMD requires a terminal; supply -- CMD...")
		}
		s, err := a.init(cmd.Context())
		if err != nil {
			return err
		}
		w, err := s.Repo.Resolve(cmd.Context(), name, s.Config.BranchPrefix)
		if err != nil {
			return err
		}
		return a.runChild(cmd.Context(), s, w.Path, child)
	}}
}

func readerTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *app) pathCommand() *cobra.Command {
	return &cobra.Command{Use: "path [NAME]", Short: "Print a worktree path", Args: atMost(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.init(cmd.Context())
		if err != nil {
			return err
		}
		w, err := s.Repo.Resolve(cmd.Context(), target(args), s.Config.BranchPrefix)
		if err != nil {
			return err
		}
		a.data = w
		if !a.json {
			_, err = fmt.Fprintln(a.stdout, w.Path)
		}
		return err
	}}
}

func (a *app) listCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List worktrees, changes, ahead counts, and processes", Args: exact(0), RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := a.init(cmd.Context())
		if err != nil {
			return err
		}
		entries, err := s.List(cmd.Context())
		if err != nil {
			return err
		}
		a.data = entries
		if a.json {
			return nil
		}
		rows := [][]string{}
		for _, e := range entries {
			name, branch, ahead, procs := e.Name, e.Branch, "-", "?"
			if e.Main {
				name += " (main)"
			}
			if branch == "" {
				branch = "(detached)"
			}
			if e.Ahead != nil {
				ahead = fmt.Sprintf("+%d", *e.Ahead)
			}
			if e.ProcessesReliable {
				procs = fmt.Sprint(e.Processes)
			}
			rows = append(rows, []string{name, branch, e.State, ahead, procs, e.Path})
		}
		return a.renderTable([]string{"NAME", "BRANCH", "STATE", "AHEAD", "PROCS", "PATH"}, rows)
	}}
}

func (a *app) psCommand() *cobra.Command {
	return &cobra.Command{Use: "ps [NAME]", Aliases: []string{"status"}, Short: "Show worktree processes, listening ports, and containers", Args: atMost(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.init(cmd.Context())
		if err != nil {
			return err
		}
		w, err := s.Repo.Resolve(cmd.Context(), target(args), s.Config.BranchPrefix)
		if err != nil {
			return err
		}
		activity, err := s.PS(cmd.Context(), w)
		if err != nil {
			return err
		}
		a.data = activity
		if a.json {
			return nil
		}
		if _, err := fmt.Fprintln(a.stdout, a.style(a.stdout, "#9ECE6A").Render(w.Path)); err != nil {
			return err
		}
		rows := [][]string{}
		for _, p := range activity.Processes {
			rows = append(rows, []string{fmt.Sprint(p.PID), strings.Join(p.Ports, ", "), p.Command})
		}
		if err := a.renderTable([]string{"PID", "LISTENING", "COMMAND"}, rows); err != nil {
			return err
		}
		rows = [][]string{}
		for _, c := range activity.Containers {
			rows = append(rows, []string{c.ID[:min(12, len(c.ID))], c.Name, fmt.Sprint(c.Running), c.Project})
		}
		return a.renderTable([]string{"CONTAINER", "NAME", "RUNNING", "COMPOSE"}, rows)
	}}
}

func (a *app) cleanupCommand(cmd *cobra.Command, args []string, action func(*worktree.Service, repo.Worktree) (worktree.CleanupResult, error)) error {
	s, err := a.init(cmd.Context())
	if err != nil {
		return err
	}
	dry, _ := cmd.Flags().GetBool("dry-run")
	if !dry {
		unlock, err := s.Lock(cmd.Context())
		if err != nil {
			return err
		}
		defer unlock()
	}
	w, err := s.Repo.Resolve(cmd.Context(), target(args), s.Config.BranchPrefix)
	if err != nil {
		return err
	}
	existed := !removedDirectory(w.Path)
	result, err := action(s, w)
	if cmd.Name() == "rm" && !dry {
		// Branch deletion can fail after Git has removed the checkout. Still
		// recover the caller's cwd, while preserving the command's failure code.
		if err == nil || (existed && removedDirectory(w.Path)) {
			a.requestCD(s, s.Repo.Main)
		}
	}
	if err == nil {
		a.data = result
		if cmd.Name() == "rm" && !dry && !a.json {
			_, err = fmt.Fprintln(a.stdout, s.Repo.Main)
		}
	}
	return err
}

func (a *app) stopCommand() *cobra.Command {
	dry := false
	c := &cobra.Command{Use: "stop [NAME]", Short: "Stop worktree processes and containers without removing files", Args: atMost(1), RunE: func(cmd *cobra.Command, args []string) error {
		return a.cleanupCommand(cmd, args, func(s *worktree.Service, w repo.Worktree) (worktree.CleanupResult, error) {
			return s.Stop(cmd.Context(), w, dry)
		})
	}}
	c.Flags().BoolVarP(&dry, "dry-run", "n", false, "Preview without running hooks, signaling, or modifying files")
	return c
}

func (a *app) removeCommand() *cobra.Command {
	o := worktree.RemoveOptions{}
	c := &cobra.Command{Use: "rm [NAME]", Aliases: []string{"remove", "done"}, Short: "Teardown, stop resources, and remove a worktree safely", Args: atMost(1), RunE: func(cmd *cobra.Command, args []string) error {
		return a.cleanupCommand(cmd, args, func(s *worktree.Service, w repo.Worktree) (worktree.CleanupResult, error) {
			return s.Remove(cmd.Context(), w, o)
		})
	}}
	f := c.Flags()
	f.BoolVarP(&o.DryRun, "dry-run", "n", false, "Preview without running hooks, signaling, or modifying files")
	f.BoolVarP(&o.Force, "force", "f", false, "Discard uncommitted work, override locks, and delete unmerged branches")
	f.BoolVar(&o.KeepBranch, "keep-branch", false, "Retain the branch after removing the worktree")
	f.BoolVar(&o.Volumes, "volumes", false, "Also remove Compose volumes (destructive)")
	return c
}

func (a *app) gcCommand() *cobra.Command {
	dry, all := false, false
	c := &cobra.Command{Use: "gc", Short: "Remove only clean, merged, idle, unlocked worktrees", Args: exact(0), RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := a.init(cmd.Context())
		if err != nil {
			return err
		}
		if !dry {
			unlock, err := s.Lock(cmd.Context())
			if err != nil {
				return err
			}
			defer unlock()
		}
		result, err := s.GC(cmd.Context(), dry, all)
		a.data = result
		return err
	}}
	c.Flags().BoolVarP(&dry, "dry-run", "n", false, "Preview without removing worktrees or resources")
	c.Flags().BoolVar(&all, "all", false, "Include worktrees outside the configured root")
	return c
}
