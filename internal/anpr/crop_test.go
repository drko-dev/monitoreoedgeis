package anpr

import (
	"bytes"
	"image/jpeg"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

func frameSpec(bbox BBox, w, h int) CropSpec {
	return CropSpec{
		RequestedBBox:   bbox,
		SourceFrameSeq:  1,
		SourceTimestamp: time.Unix(1, 0),
		SourceWidth:     w,
		SourceHeight:    h,
		EncodingIntent:  "jpeg",
	}
}

// B5: crop clamps to frame — a bbox partially outside the frame is clamped,
// never left out of bounds, never dropped.
func TestB5_CropClampsToFrame(t *testing.T) {
	spec := frameSpec(BBox{X0: -50, Y0: -50, X1: 50, Y1: 50}, 100, 100)
	res, err := ComputeCrop(spec)
	if err != nil {
		t.Fatalf("expected clamp, got error: %v", err)
	}
	if res.ClampedBBox.X0 != 0 || res.ClampedBBox.Y0 != 0 {
		t.Fatalf("expected clamp to 0,0, got %+v", res.ClampedBBox)
	}
	if res.ClampedBBox.X1 != 50 || res.ClampedBBox.Y1 != 50 {
		t.Fatalf("expected far edge preserved, got %+v", res.ClampedBBox)
	}

	// Vehicle partially outside the frame on the far edge too.
	spec2 := frameSpec(BBox{X0: 80, Y0: 80, X1: 150, Y1: 150}, 100, 100)
	res2, err := ComputeCrop(spec2)
	if err != nil {
		t.Fatalf("expected clamp, got error: %v", err)
	}
	if res2.ClampedBBox.X1 != 100 || res2.ClampedBBox.Y1 != 100 {
		t.Fatalf("expected clamp to frame edge, got %+v", res2.ClampedBBox)
	}
}

// B6: zero-area crop rejected — explicit error, never panic, never a
// silently-empty CropResult.
func TestB6_ZeroAreaCropRejected(t *testing.T) {
	cases := map[string]CropSpec{
		"zero width":     frameSpec(BBox{X0: 10, Y0: 10, X1: 10, Y1: 20}, 100, 100),
		"zero height":    frameSpec(BBox{X0: 10, Y0: 10, X1: 20, Y1: 10}, 100, 100),
		"fully negative": frameSpec(BBox{X0: -50, Y0: -50, X1: -10, Y1: -10}, 100, 100),
		"fully beyond":   frameSpec(BBox{X0: 150, Y0: 150, X1: 200, Y1: 200}, 100, 100),
	}
	for name, spec := range cases {
		if _, err := ComputeCrop(spec); err == nil {
			t.Fatalf("%s: expected rejection, got success", name)
		}
	}
}

// Additional adversarial cases from the spec's crop-safety checklist:
// negative coords, inverted bbox, tiny bbox, full frame — never panic.
func TestCropSafetyChecklist(t *testing.T) {
	t.Run("negative coordinates", func(t *testing.T) {
		_, err := ComputeCrop(frameSpec(BBox{X0: -10, Y0: -10, X1: 30, Y1: 30}, 100, 100))
		if err != nil {
			t.Fatalf("expected clamp success, got %v", err)
		}
	})
	t.Run("inverted bbox", func(t *testing.T) {
		_, err := ComputeCrop(frameSpec(BBox{X0: 50, Y0: 50, X1: 10, Y1: 10}, 100, 100))
		if err != ErrCropInvertedBBox {
			t.Fatalf("expected ErrCropInvertedBBox, got %v", err)
		}
	})
	t.Run("tiny bbox", func(t *testing.T) {
		res, err := ComputeCrop(frameSpec(BBox{X0: 10, Y0: 10, X1: 11, Y1: 11}, 100, 100))
		if err != nil {
			t.Fatalf("expected tiny-but-valid crop to succeed, got %v", err)
		}
		if res.OutputWidth != 1 || res.OutputHeight != 1 {
			t.Fatalf("expected 1x1 output, got %dx%d", res.OutputWidth, res.OutputHeight)
		}
	})
	t.Run("full frame", func(t *testing.T) {
		res, err := ComputeCrop(frameSpec(BBox{X0: 0, Y0: 0, X1: 100, Y1: 100}, 100, 100))
		if err != nil {
			t.Fatalf("expected full-frame crop to succeed, got %v", err)
		}
		if res.OutputWidth != 100 || res.OutputHeight != 100 {
			t.Fatalf("expected full frame output, got %dx%d", res.OutputWidth, res.OutputHeight)
		}
	})
	t.Run("invalid frame dimensions never panics", func(t *testing.T) {
		_, err := ComputeCrop(frameSpec(BBox{X0: 0, Y0: 0, X1: 10, Y1: 10}, 0, 0))
		if err != ErrCropInvalidFrame {
			t.Fatalf("expected ErrCropInvalidFrame, got %v", err)
		}
	})
}

// B7: context crop bounded — padding expands the bbox but the result is
// still clamped to the frame, never grows past its edges.
func TestB7_ContextCropBounded(t *testing.T) {
	spec := frameSpec(BBox{X0: 40, Y0: 40, X1: 60, Y1: 60}, 100, 100)
	spec.Padding = Padding{PercentX: 5, PercentY: 5} // wildly oversized padding
	res, err := ComputeCrop(spec)
	if err != nil {
		t.Fatalf("expected bounded padded crop to succeed, got %v", err)
	}
	if res.ClampedBBox.X0 < 0 || res.ClampedBBox.Y0 < 0 || res.ClampedBBox.X1 > 100 || res.ClampedBBox.Y1 > 100 {
		t.Fatalf("expected clamp to frame edges, got %+v", res.ClampedBBox)
	}
}

// CropPolicy selection.
func TestSelectCropBBoxPolicies(t *testing.T) {
	vehicle := BBox{X0: 0, Y0: 0, X1: 100, Y1: 100}
	plate := BBox{X0: 40, Y0: 60, X1: 60, Y1: 80}

	if b, err := SelectCropBBox(CropVehicleContext, vehicle, nil); err != nil || b != vehicle {
		t.Fatalf("vehicle_context: got %+v, %v", b, err)
	}
	if _, err := SelectCropBBox(CropPlateOnly, vehicle, nil); err == nil {
		t.Fatal("plate_only with no plate bbox should fail")
	}
	if b, err := SelectCropBBox(CropPlateOnly, vehicle, &plate); err != nil || b != plate {
		t.Fatalf("plate_only: got %+v, %v", b, err)
	}
	if b, err := SelectCropBBox(CropPlatePlusContext, vehicle, nil); err != nil || b != vehicle {
		t.Fatalf("plate_plus_context degrade: got %+v, %v", b, err)
	}
	if b, err := SelectCropBBox(CropPlatePlusContext, vehicle, &plate); err != nil || b != plate {
		t.Fatalf("plate_plus_context with plate: got %+v, %v", b, err)
	}
}

// B31: frame ownership safe — ExtractJPEG copies pixel data into its output
// before returning; a caller mutating frame.Data right after the call must
// never change the already-returned crop bytes.
func TestB31_FrameOwnershipSafe(t *testing.T) {
	w, h := 16, 16
	data := makeYUV420P(w, h, 128) // mid-gray
	frame := processing.Frame{Data: data, OutputWidth: w, OutputHeight: h}

	result, err := ComputeCrop(frameSpec(BBox{X0: 2, Y0: 2, X1: 10, Y1: 10}, w, h))
	if err != nil {
		t.Fatalf("ComputeCrop: %v", err)
	}

	jpeg1, err := ExtractJPEG(frame, result)
	if err != nil {
		t.Fatalf("ExtractJPEG: %v", err)
	}
	if len(jpeg1) == 0 {
		t.Fatal("expected non-empty jpeg output")
	}

	img1, err := jpeg.Decode(bytes.NewReader(jpeg1))
	if err != nil {
		t.Fatalf("decode jpeg1: %v", err)
	}
	r1, g1, b1, _ := img1.At(0, 0).RGBA()

	// Mutate the source buffer AFTER ExtractJPEG already returned. A safe
	// (copy-before-return) implementation leaves jpeg1's decoded pixels
	// unchanged; an aliasing bug would show up as a changed pixel here.
	for i := range data {
		data[i] = 0xFF
	}

	img1Again, err := jpeg.Decode(bytes.NewReader(jpeg1))
	if err != nil {
		t.Fatalf("re-decode jpeg1: %v", err)
	}
	r2, g2, b2, _ := img1Again.At(0, 0).RGBA()
	if r1 != r2 || g1 != g2 || b1 != b2 {
		t.Fatal("jpeg1's pixel data changed after mutating frame.Data post-return — output aliases the source frame buffer")
	}
}

// makeYUV420P builds a packed yuv420p buffer of value `fill` for every
// plane byte — enough for processing.YUV420PToImage to decode without
// error.
func makeYUV420P(w, h int, fill byte) []byte {
	ySize := w * h
	cSize := (w / 2) * (h / 2)
	buf := make([]byte, ySize+2*cSize)
	for i := range buf {
		buf[i] = fill
	}
	return buf
}

// Hito J6 hybrid wiring: ExtractJPEGFromEncoded crops an already-encoded
// full-frame JPEG (what vision.EventConsumer.ConsumeInference actually
// receives) instead of a raw processing.Frame.
func TestExtractJPEGFromEncoded_ProducesValidCrop(t *testing.T) {
	w, h := 32, 32
	frame := processing.Frame{Data: makeYUV420P(w, h, 128), OutputWidth: w, OutputHeight: h}
	fullFrameJPEG, err := ExtractJPEG(frame, mustCropResult(t, BBox{X0: 0, Y0: 0, X1: float64(w), Y1: float64(h)}, w, h))
	if err != nil {
		t.Fatalf("build fixture full-frame jpeg: %v", err)
	}

	result := mustCropResult(t, BBox{X0: 4, Y0: 4, X1: 12, Y1: 12}, w, h)
	cropped, err := ExtractJPEGFromEncoded(fullFrameJPEG, result)
	if err != nil {
		t.Fatalf("ExtractJPEGFromEncoded: %v", err)
	}
	if len(cropped) == 0 {
		t.Fatal("expected non-empty jpeg output")
	}
	img, err := jpeg.Decode(bytes.NewReader(cropped))
	if err != nil {
		t.Fatalf("decode cropped jpeg: %v", err)
	}
	if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 8 {
		t.Fatalf("expected 8x8 crop, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

// Frame ownership (item 19/74): mutating the source slice after the call
// must never change the already-returned crop bytes.
func TestExtractJPEGFromEncoded_FrameOwnershipSafe(t *testing.T) {
	w, h := 32, 32
	frame := processing.Frame{Data: makeYUV420P(w, h, 128), OutputWidth: w, OutputHeight: h}
	fullFrameJPEG, err := ExtractJPEG(frame, mustCropResult(t, BBox{X0: 0, Y0: 0, X1: float64(w), Y1: float64(h)}, w, h))
	if err != nil {
		t.Fatalf("build fixture full-frame jpeg: %v", err)
	}

	result := mustCropResult(t, BBox{X0: 2, Y0: 2, X1: 10, Y1: 10}, w, h)
	cropped1, err := ExtractJPEGFromEncoded(fullFrameJPEG, result)
	if err != nil {
		t.Fatalf("ExtractJPEGFromEncoded: %v", err)
	}

	for i := range fullFrameJPEG {
		fullFrameJPEG[i] = 0x00
	}

	cropped2 := append([]byte(nil), cropped1...)
	if !bytes.Equal(cropped1, cropped2) {
		t.Fatal("cropped1 was mutated after the fact -- should be an independent copy")
	}
}

func TestExtractJPEGFromEncoded_MalformedInputRejected(t *testing.T) {
	result := mustCropResult(t, BBox{X0: 0, Y0: 0, X1: 8, Y1: 8}, 32, 32)
	if _, err := ExtractJPEGFromEncoded([]byte("not a jpeg"), result); err == nil {
		t.Fatal("expected an error decoding malformed jpeg, got nil")
	}
}

func mustCropResult(t *testing.T, bbox BBox, w, h int) CropResult {
	t.Helper()
	res, err := ComputeCrop(frameSpec(bbox, w, h))
	if err != nil {
		t.Fatalf("ComputeCrop: %v", err)
	}
	return res
}
