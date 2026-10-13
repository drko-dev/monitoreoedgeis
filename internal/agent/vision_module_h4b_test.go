package agent

import (
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/vision"
)

type syntheticBundledLaunch struct {
	verified bool
}

func (syntheticBundledLaunch) ModelsAuthorized() bool      { return false }
func (s syntheticBundledLaunch) ArtifactsVerified() bool   { return s.verified }
func (syntheticBundledLaunch) RuntimePackRootPath() string { return "/pack" }
func (syntheticBundledLaunch) CommandPath() string         { return "/pack/python/bin/python3" }
func (syntheticBundledLaunch) CommandArgs() []string {
	return []string{"-I", "-B", "/pack/worker/worker.py"}
}
func (syntheticBundledLaunch) ProcessEnvironment() []string       { return []string{"PATH=/pack/media"} }
func (syntheticBundledLaunch) PersistentModelsDir() string        { return "/data/models" }
func (syntheticBundledLaunch) ProcessStartTimeout() time.Duration { return 5 * time.Second }

func TestBuildBundledVisionSinkRequiresVerifiedContract(t *testing.T) {
	cfg := &config.Config{EdgeYOLOPersonModel: "person.pt", EdgeYOLOVehicleModel: "vehicle.pt"}
	if sink, module, err := buildBundledVisionSink(cfg, nil, nil, syntheticBundledLaunch{}, nil); err == nil || sink != nil || module != nil {
		t.Fatalf("unverified Runtime Pack must not produce a worker: sink=%v module=%v err=%v", sink, module, err)
	}
}

func TestBundledVisionSinkFailsClosedUntilModelsAreAuthorized(t *testing.T) {
	cfg := &config.Config{EdgeYOLOPersonModel: "person.pt", EdgeYOLOVehicleModel: "vehicle.pt"}
	sink, module, err := buildBundledVisionSink(cfg, nil, nil, syntheticBundledLaunch{verified: true}, nil)
	if sink != nil || module != nil || err != vision.ErrHermeticModelPackUnauthorized {
		t.Fatalf("expected model authorization gate before worker construction, sink=%v module=%v err=%v", sink, module, err)
	}
}
