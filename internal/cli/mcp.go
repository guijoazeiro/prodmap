package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/mcpserver"
)

func writeMCPUsage(writer interface{ Write([]byte) (int, error) }) {
	fmt.Fprintln(writer, "usage: prodmap mcp serve [--project-dir <path>] [--data-dir <path>]")
}

func (a *App) runMCP(ctx context.Context, args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		writeMCPUsage(a.Stdout)
		return nil
	}
	if len(args) != 0 && args[0] != "serve" {
		return fmt.Errorf("unknown mcp subcommand: %w", errs.ErrInvalid)
	}
	if len(args) == 0 {
		return fmt.Errorf("mcp subcommand is required: %w", errs.ErrInvalid)
	}
	flags := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	flags.Usage = func() { writeMCPUsage(flags.Output()) }
	common := addInventoryFlags(flags)
	help, err := a.parseCommandFlags(flags, args[1:])
	if help {
		return nil
	}
	if err != nil || flags.NArg() != 0 || *common.jsonOutput {
		return fmt.Errorf("invalid mcp serve flags: %w", errs.ErrInvalid)
	}
	_, store, err := a.openInventoryReadOnly(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	return mcpserver.RunStdio(ctx, mcpserver.Config{Reader: store, Now: a.Now})
}
