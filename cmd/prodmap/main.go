package main

import (
	"context"
	"os"

	"github.com/guijoazeiro/prodmap/internal/cli"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	info := cli.DefaultBuildInfo()
	info.Version = version
	info.Commit = commit
	info.BuildDate = buildDate
	app := cli.NewApp(os.Stdout, os.Stderr, info)
	os.Exit(app.Run(context.Background(), os.Args[1:]))
}
