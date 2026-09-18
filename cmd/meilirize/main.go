package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"meilirize/internal/buildinfo"
	"meilirize/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := cli.Execute(
		ctx,
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
		buildinfo.Current(),
	)
	if err != nil {
		cli.PrintError(os.Stderr, err)
		os.Exit(1)
	}
}
