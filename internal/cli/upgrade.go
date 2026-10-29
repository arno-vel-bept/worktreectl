package cli

import (
	"fmt"
	"os"

	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
	"github.com/arno-vel-bept/worktreectl/internal/update"
	"github.com/spf13/cobra"
)

func (a *app) upgradeCommand() *cobra.Command {
	options := update.Options{Repo: os.Getenv("WORKTREECTL_REPO")}
	if options.Repo == "" {
		options.Repo = update.DefaultRepo
	}
	cmd := &cobra.Command{
		Use: "upgrade", Aliases: []string{"update"}, Short: "Upgrade this executable to the latest checksum-verified release",
		Long: "Upgrade this executable in place without changing project files or shell configuration.\nDefaults to the latest stable release. Works outside Git repositories; never needs Go or curl.\nUse --check to inspect without downloading or changing files, or --version to select an exact release.",
		Args: exact(0), RunE: func(cmd *cobra.Command, _ []string) error {
			if !update.ValidRepo(options.Repo) {
				return usage("invalid release repository %q; expected OWNER/REPO", options.Repo)
			}
			if options.Version != "" && !update.ValidVersion(options.Version) {
				return usage("invalid release version %q; use --version vX.Y.Z", options.Version)
			}
			path, err := os.Executable()
			if err != nil {
				return diagnostic.New("cannot locate this executable for upgrade", "Use install.sh to reinstall into your bin directory.", err)
			}
			options.Path = path
			result, err := update.Upgrade(cmd.Context(), a.build.Version, options)
			a.data = result
			if err != nil || a.json {
				return err
			}
			switch {
			case result.Updated:
				_, err = fmt.Fprintf(a.stdout, "Upgraded worktreectl %s → %s at %s\n", result.CurrentVersion, result.TargetVersion, result.Path)
			case result.Available:
				_, err = fmt.Fprintf(a.stdout, "worktreectl %s is available (installed: %s). Run worktreectl upgrade to install it.\n", result.TargetVersion, result.CurrentVersion)
			default:
				_, err = fmt.Fprintf(a.stdout, "worktreectl %s is up to date (latest release: %s).\n", result.CurrentVersion, result.TargetVersion)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&options.Check, "check", false, "Check release availability without changing or downloading the executable")
	cmd.Flags().StringVar(&options.Version, "version", "", "Install an exact release (explicitly allows choosing an older version)")
	cmd.Flags().StringVar(&options.Repo, "repo", options.Repo, "Trusted GitHub release repository (OWNER/REPO)")
	return cmd
}
