package main

import (
	"context"
	"os"

	"meilirize/internal/buildinfo"
	"meilirize/internal/cli"
)

func main() {
	err := cli.Execute(
		context.Background(),
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
