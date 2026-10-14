package agent

import (
	"strings"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

func TestNewHermeticRequiresExplicitFullEdgeAndVerifiedBinary(t *testing.T) {
	if _, err := NewHermetic(nil, processing.VerifiedFFmpeg{}); err == nil {
		t.Fatal("expected nil config to fail")
	}
	if _, err := NewHermetic(&config.Config{ProcessingMode: config.ModeCloud}, processing.VerifiedFFmpeg{}); err == nil || !strings.Contains(err.Error(), "Full Edge") {
		t.Fatalf("non-Edge mode error = %v, want Full Edge rejection", err)
	}
	if _, err := NewHermetic(&config.Config{ProcessingMode: config.ModeEdge}, processing.VerifiedFFmpeg{}); err == nil || !strings.Contains(err.Error(), "Runtime Pack FFmpeg") {
		t.Fatalf("empty capability error = %v, want verified Runtime Pack rejection", err)
	}
}
