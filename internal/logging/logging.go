// Package logging builds the agent logger on top of stdlib log/slog.
//
// Output goes to stdout in a format suited to terminals, containers, systemd
// and Kubernetes log collection. No secrets are ever logged.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a slog.Logger at the given level, tagged with the base fields
// shared by every component.
func New(level, version, edgeID, processingMode string) *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(level)})

	logger := slog.New(handler).With(
		slog.String("version", version),
		slog.String("processing_mode", processingMode),
	)
	if edgeID != "" {
		logger = logger.With(slog.String("edge_id", edgeID))
	}
	return logger
}

// Component returns a child logger tagged with a component name.
func Component(l *slog.Logger, name string) *slog.Logger {
	return l.With(slog.String("component", name))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
