package vision

import (
	"log/slog"
	"strconv"
)

func itoa(n int) string     { return strconv.Itoa(n) }
func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// slogWriter adapts the worker subprocess's stdout/stderr to structured
// logging, one line at a time, so a worker crash's stderr ends up in the
// same log stream as everything else — never left to a stray unbuffered
// pipe. It never logs frame bytes or credentials: the worker's own protocol
// carries neither on stdout/stderr, only its own diagnostic text.
type slogWriter struct {
	logger *slog.Logger
	stream string
}

func (s *slogWriter) Write(p []byte) (int, error) {
	s.logger.Info("edge vision worker output", slog.String("stream", s.stream), slog.String("line", string(p)))
	return len(p), nil
}
