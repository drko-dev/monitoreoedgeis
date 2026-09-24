// Package logging builds the agent logger on top of stdlib log/slog.
//
// Output goes to stdout in a format suited to terminals, containers, systemd
// and Kubernetes log collection. No secrets are ever logged.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	outputMu    sync.Mutex
	outputFiles []*os.File
)

// New returns a slog.Logger at the given level, tagged with the base fields
// shared by every component.
func New(level, version, edgeID, processingMode string) *slog.Logger {
	var output io.Writer = os.Stdout
	if path := strings.TrimSpace(os.Getenv("GEOCAM_LOG_FILE")); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "geocam-edge: create log directory failed (%s)\n", typeName(err))
		} else if file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "geocam-edge: open log file failed (%s)\n", typeName(err))
		} else {
			if err := file.Chmod(0o600); err != nil {
				_ = file.Close()
				_, _ = fmt.Fprintf(os.Stderr, "geocam-edge: secure log file failed (%s)\n", typeName(err))
			} else {
				outputMu.Lock()
				outputFiles = append(outputFiles, file)
				outputMu.Unlock()
				output = file
			}
		}
	}
	handler := slog.NewTextHandler(output, &slog.HandlerOptions{Level: parseLevel(level)})

	logger := slog.New(handler).With(
		slog.String("version", version),
		slog.String("processing_mode", processingMode),
	)
	if edgeID != "" {
		logger = logger.With(slog.String("edge_id", edgeID))
	}
	return logger
}

// Close releases files opened by New. Service entrypoints call it after
// graceful shutdown; abrupt process termination releases descriptors in the OS.
func Close() error {
	outputMu.Lock()
	files := outputFiles
	outputFiles = nil
	outputMu.Unlock()
	var first error
	for _, file := range files {
		if err := file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func typeName(err error) string {
	return fmt.Sprintf("%T", err)
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
