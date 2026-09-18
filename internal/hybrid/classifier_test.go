package hybrid

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

func TestNoopClassifier(t *testing.T) {
	c := &NoopClassifier{}
	defer c.Close()

	frame := &processing.Frame{
		CandidateKey: "cam-1",
		Seq:          100,
		Timestamp:    time.Now(),
		OutputWidth:  640,
		OutputHeight: 480,
	}

	res, err := c.Classify(context.Background(), frame)
	if err != nil {
		t.Fatalf("unexpected error from NoopClassifier: %v", err)
	}
	if res.IsCandidate {
		t.Errorf("expected IsCandidate=false, got true")
	}
	if res.Reason != "noop_disabled" {
		t.Errorf("expected reason 'noop_disabled', got %q", res.Reason)
	}
}

func TestNewClassifier(t *testing.T) {
	// 1. Default disabled
	c1, err := NewClassifier(Config{Enabled: false})
	if err != nil {
		t.Fatalf("NewClassifier disabled failed: %v", err)
	}
	if _, ok := c1.(*NoopClassifier); !ok {
		t.Errorf("expected *NoopClassifier, got %T", c1)
	}

	// 2. Enabled but empty model path defaults to NoopClassifier
	c2, err := NewClassifier(Config{Enabled: true, ModelPath: ""})
	if err != nil {
		t.Fatalf("NewClassifier empty path failed: %v", err)
	}
	if _, ok := c2.(*NoopClassifier); !ok {
		t.Errorf("expected *NoopClassifier, got %T", c2)
	}

	// 3. Enabled with unconfigured real model returns ErrUnsupportedClassifier
	_, err = NewClassifier(Config{Enabled: true, ModelPath: "/path/to/model.onnx"})
	if !errors.Is(err, ErrUnsupportedClassifier) {
		t.Errorf("expected ErrUnsupportedClassifier, got %v", err)
	}
}
