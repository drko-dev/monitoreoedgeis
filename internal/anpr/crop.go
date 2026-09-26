package anpr

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// CropPolicy selects what region(s) a candidate's crop covers. Configurable
// per Config, never hardcoded to one strategy (spec item 8).
type CropPolicy string

const (
	// CropPlateOnly crops exactly the plate region. Requires a non-nil
	// PlateBBox; a caller using this policy with no plate region is a
	// UNAVAILABLE case (see registry.go), not a silent fallback.
	CropPlateOnly CropPolicy = "plate_only"
	// CropVehicleContext crops the vehicle bbox (plus configured padding),
	// regardless of whether a plate region is known.
	CropVehicleContext CropPolicy = "vehicle_context"
	// CropPlatePlusContext crops the vehicle bbox (wider padding, "context")
	// when a plate region is unavailable, or the plate bbox when available —
	// i.e. it degrades to vehicle context rather than failing, since a wider
	// shot is still useful evidence. This degrade behavior is the one
	// explicit policy choice PREP makes for this mode; a future OCR-tuned
	// strategy can replace it without changing the type.
	CropPlatePlusContext CropPolicy = "plate_plus_context"
)

// Padding expands a bbox before clamping. Exactly one of the two forms is
// meant to be used per config (percentage OR pixels); both can be set (they
// stack) but a caller normally picks one.
type Padding struct {
	// PercentX/PercentY expand the bbox by this fraction of its own
	// width/height on each side (0.1 == 10% each side, i.e. 20% wider).
	PercentX, PercentY float64
	// PixelsX/PixelsY expand the bbox by this many pixels on each side.
	PixelsX, PixelsY float64
}

func (p Padding) apply(b BBox) BBox {
	dx := b.Width()*p.PercentX + p.PixelsX
	dy := b.Height()*p.PercentY + p.PixelsY
	return BBox{X0: b.X0 - dx, Y0: b.Y0 - dy, X1: b.X1 + dx, Y1: b.Y1 + dy}
}

// CropSpec describes a requested crop before it is checked against frame
// bounds.
type CropSpec struct {
	RequestedBBox   BBox
	Padding         Padding
	SourceFrameSeq  uint64
	SourceTimestamp time.Time
	SourceWidth     int
	SourceHeight    int
	// EncodingIntent documents what the crop will be encoded as downstream
	// ("jpeg" is the only intent PREP implements — see ExtractJPEG).
	EncodingIntent string
}

// CropResult is the outcome of a bounds-checked crop: never contains an
// out-of-frame bbox. Errors are returned instead of ever emitting a
// CropResult with a nonsensical (inverted/zero-area) bbox.
type CropResult struct {
	RequestedBBox   BBox
	ClampedBBox     BBox
	OutputWidth     int
	OutputHeight    int
	SourceFrameSeq  uint64
	SourceTimestamp time.Time
	EncodingIntent  string
	Quality         QualityHints
}

// Crop errors. ComputeCrop never panics; every rejection is one of these.
var (
	ErrCropInvertedBBox = errors.New("anpr: crop bbox inverted")
	ErrCropZeroArea     = errors.New("anpr: crop bbox has zero area after clamping")
	ErrCropInvalidFrame = errors.New("anpr: crop source frame has non-positive dimensions")
	ErrCropFullyOutside = errors.New("anpr: crop bbox is fully outside the frame")
)

// ComputeCrop validates spec, applies padding, clamps the padded bbox to
// [0,SourceWidth]x[0,SourceHeight], and returns the resulting CropResult.
// It never returns an out-of-frame bbox and never panics:
//   - a structurally inverted requested bbox is rejected (ErrCropInvertedBBox)
//   - a bbox that clamps to zero width or height is rejected (ErrCropZeroArea)
//   - a bbox entirely outside the frame is rejected (ErrCropFullyOutside)
//   - partial overlap is CLAMPED to the frame edges (documented policy —
//     never silently dropped, never left out-of-bounds)
func ComputeCrop(spec CropSpec) (CropResult, error) {
	if spec.SourceWidth <= 0 || spec.SourceHeight <= 0 {
		return CropResult{}, ErrCropInvalidFrame
	}
	if !spec.RequestedBBox.Valid() {
		return CropResult{}, ErrCropInvertedBBox
	}

	padded := spec.Padding.apply(spec.RequestedBBox)
	if !padded.Valid() {
		return CropResult{}, ErrCropInvertedBBox
	}

	fw, fh := float64(spec.SourceWidth), float64(spec.SourceHeight)
	if padded.X1 <= 0 || padded.Y1 <= 0 || padded.X0 >= fw || padded.Y0 >= fh {
		return CropResult{}, ErrCropFullyOutside
	}

	clamped := BBox{
		X0: clampF(padded.X0, 0, fw),
		Y0: clampF(padded.Y0, 0, fh),
		X1: clampF(padded.X1, 0, fw),
		Y1: clampF(padded.Y1, 0, fh),
	}
	if clamped.Width() <= 0 || clamped.Height() <= 0 {
		return CropResult{}, ErrCropZeroArea
	}

	return CropResult{
		RequestedBBox:   spec.RequestedBBox,
		ClampedBBox:     clamped,
		OutputWidth:     int(clamped.Width()),
		OutputHeight:    int(clamped.Height()),
		SourceFrameSeq:  spec.SourceFrameSeq,
		SourceTimestamp: spec.SourceTimestamp,
		EncodingIntent:  spec.EncodingIntent,
	}, nil
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// SelectCropBBox implements CropPolicy: which bbox a caller should build a
// CropSpec from. plateBBox may be nil (no plate region available).
func SelectCropBBox(policy CropPolicy, vehicleBBox BBox, plateBBox *BBox) (BBox, error) {
	switch policy {
	case CropPlateOnly:
		if plateBBox == nil {
			return BBox{}, errors.New("anpr: plate_only policy requires a plate bbox")
		}
		return *plateBBox, nil
	case CropVehicleContext:
		return vehicleBBox, nil
	case CropPlatePlusContext:
		if plateBBox != nil {
			return *plateBBox, nil
		}
		return vehicleBBox, nil
	default:
		return BBox{}, fmt.Errorf("anpr: unknown crop policy %q", policy)
	}
}

// jpegQuality mirrors internal/vision's fixed inference-only JPEG quality
// choice (see internal/vision/sink.go) — this package encodes crops for
// transport/OCR review, not for display, so a further configurable knob
// isn't needed in PREP.
const jpegQuality = 90

// ExtractJPEG decodes frame.Data (yuv420p, per processing.YUV420PToImage),
// crops it to result.ClampedBBox and JPEG-encodes the crop, all
// synchronously within this one call.
//
// Frame ownership (spec item 19): this function never retains frame.Data
// past its own return, and never returns a slice backed by frame.Data's
// underlying array — the crop is copied into a fresh image.RGBA before
// encoding, and jpeg.Encode's output is a fresh []byte. A caller may safely
// reuse or mutate frame.Data immediately after ExtractJPEG returns.
func ExtractJPEG(frame processing.Frame, result CropResult) ([]byte, error) {
	img, err := processing.YUV420PToImage(frame.Data, frame.OutputWidth, frame.OutputHeight)
	if err != nil {
		return nil, fmt.Errorf("anpr: decode source frame: %w", err)
	}
	return cropImageToJPEG(img, result)
}

// ExtractJPEGFromEncoded crops an ALREADY JPEG-encoded full frame -- the
// bytes vision.EventConsumer.ConsumeInference actually receives, since the
// raw processing.Frame is not passed to that callback -- to
// result.ClampedBBox and re-encodes the crop. Shares cropImageToJPEG with
// ExtractJPEG rather than duplicating the rect-clamp/draw/encode logic;
// only the decode step differs.
//
// Same frame-ownership guarantee as ExtractJPEG (item 19/74): the returned
// bytes never alias fullFrameJPEG's backing array.
func ExtractJPEGFromEncoded(fullFrameJPEG []byte, result CropResult) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(fullFrameJPEG))
	if err != nil {
		return nil, fmt.Errorf("anpr: decode source jpeg: %w", err)
	}
	return cropImageToJPEG(img, result)
}

// cropImageToJPEG crops img to result.ClampedBBox and JPEG-encodes the
// crop. Shared by both ExtractJPEG entry points.
func cropImageToJPEG(img image.Image, result CropResult) ([]byte, error) {
	rect := image.Rect(
		int(result.ClampedBBox.X0), int(result.ClampedBBox.Y0),
		int(result.ClampedBBox.X0)+result.OutputWidth, int(result.ClampedBBox.Y0)+result.OutputHeight,
	)
	// Intersect defensively against the decoded image's actual bounds, in
	// case the crop's source dimensions ever disagree with it — never
	// index out of range.
	rect = rect.Intersect(img.Bounds())
	if rect.Empty() {
		return nil, ErrCropZeroArea
	}

	// Copy (not SubImage-and-share) so the returned bytes never alias the
	// source image's backing array.
	cropped := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(cropped, cropped.Bounds(), img, rect.Min, draw.Src)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("anpr: jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
}
