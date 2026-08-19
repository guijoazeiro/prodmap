// Package logging constructs configured structured loggers without global state.
package logging

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

type Options struct{ Level, Format string }

func New(w io.Writer, opts Options) (*slog.Logger, error) {
	if w == nil {
		return nil, fmt.Errorf("log writer must not be nil: %w", errs.ErrInvalid)
	}
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	level, ok := levels[opts.Level]
	if !ok {
		return nil, fmt.Errorf("invalid log level %q: %w", opts.Level, errs.ErrInvalid)
	}
	handlerOpts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch opts.Format {
	case "text":
		handler = slog.NewTextHandler(w, handlerOpts)
	case "json":
		handler = slog.NewJSONHandler(w, handlerOpts)
	default:
		return nil, fmt.Errorf("invalid log format %q: %w", opts.Format, errs.ErrInvalid)
	}
	return slog.New(handler), nil
}
