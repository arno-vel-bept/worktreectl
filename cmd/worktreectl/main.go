package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/arno-vel-bept/worktreectl/internal/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(cli.Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, cli.BuildInfo{
		Version: version, Commit: commit, Date: date,
	}))
}
