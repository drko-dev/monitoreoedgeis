package anpr

import "errors"

// ErrPlateRegionUnavailable is returned by a PlateRegionProvider that has no
// answer for this frame (model not loaded, no plate found, provider
// disabled) — a normal, expected outcome in PREP, never a panic.
var ErrPlateRegionUnavailable = errors.New("anpr: plate region unavailable")

// PlateRegionProvider locates a plate region within a vehicle candidate's
// bbox. Spec item 5 is explicit that PREP ships NO real plate detector
// model — only this interface plus non-productive implementations below.
// A real Ultralytics-backed provider is future (J6-C+) integration work.
type PlateRegionProvider interface {
	// LocatePlate returns the plate bbox for candidate within frame bounds
	// (sourceWidth x sourceHeight), or ErrPlateRegionUnavailable if it
	// cannot produce one. It never panics and never fabricates a
	// low-confidence guess dressed up as a real detection.
	LocatePlate(candidate VehicleCandidate, sourceWidth, sourceHeight int) (BBox, error)
}

// NoneProvider always reports unavailable — the safe PREP default when no
// plate-region provider is wired at all. Callers fall back to
// vehicle/context crop policy (see CropPolicy) rather than failing the
// whole candidate.
type NoneProvider struct{}

func (NoneProvider) LocatePlate(VehicleCandidate, int, int) (BBox, error) {
	return BBox{}, ErrPlateRegionUnavailable
}

// FakePlateRegionProvider is a deterministic, clearly-labeled test double:
// it derives a plate-sized sub-rectangle from the vehicle bbox using a
// fixed, documented heuristic (lower-middle third of the vehicle box,
// mimicking where a plate typically sits) — NEVER sold as, or mistakeable
// for, a productive plate detector. It exists purely so burst/crop/quality
// code has something deterministic to exercise in tests without a real
// model.
type FakePlateRegionProvider struct {
	// Unavailable, when true, makes LocatePlate always fail — for exercising
	// the fail-closed "provider unavailable" path (B23) with the same type.
	Unavailable bool
}

func (f FakePlateRegionProvider) LocatePlate(v VehicleCandidate, sourceWidth, sourceHeight int) (BBox, error) {
	if f.Unavailable {
		return BBox{}, ErrPlateRegionUnavailable
	}
	vb := v.VehicleBBox
	if !vb.Valid() {
		return BBox{}, ErrPlateRegionUnavailable
	}
	w := vb.Width()
	h := vb.Height()
	// Heuristic reference rectangle only — see doc comment above.
	plate := BBox{
		X0: vb.X0 + w*0.25,
		X1: vb.X0 + w*0.75,
		Y0: vb.Y0 + h*0.70,
		Y1: vb.Y0 + h*0.90,
	}
	// Clamp to the frame and to the vehicle bbox itself; a heuristic must
	// never invent a region outside either.
	fw, fh := float64(sourceWidth), float64(sourceHeight)
	plate.X0 = clampF(plate.X0, 0, fw)
	plate.Y0 = clampF(plate.Y0, 0, fh)
	plate.X1 = clampF(plate.X1, 0, fw)
	plate.Y1 = clampF(plate.Y1, 0, fh)
	if !plate.Valid() {
		return BBox{}, ErrPlateRegionUnavailable
	}
	return plate, nil
}
