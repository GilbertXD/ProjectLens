package logger

import (
	"io"
	"log/slog"

	"projectlens/internal/config"
)

func New(cfg config.LogConfig, out io.Writer) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: level}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(out, options))
	}
	return slog.New(slog.NewTextHandler(out, options))
}
