package processing

import (
	"fmt"
	"image"
)

// YUV420PToImage wraps a packed yuv420p buffer (no row padding — exactly
// what ffmpeg_decoder.go's rawvideo output and resizer.go's resize produce)
// as an *image.YCbCr without copying pixel data. jpeg.Encode accepts
// *image.YCbCr directly, so this never round-trips through RGB. Shared by
// internal/cloudsink (Milestone I) and internal/vision (Milestone K) so
// there is exactly one yuv420p->image conversion in the codebase.
func YUV420PToImage(data []byte, width, height int) (*image.YCbCr, error) {
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return nil, fmt.Errorf("invalid frame dimensions %dx%d", width, height)
	}
	ySize := width * height
	cSize := (width / 2) * (height / 2)
	want := ySize + 2*cSize
	if len(data) != want {
		return nil, fmt.Errorf("frame data length %d does not match %dx%d yuv420p (want %d)", len(data), width, height, want)
	}
	return &image.YCbCr{
		Y:              data[:ySize],
		Cb:             data[ySize : ySize+cSize],
		Cr:             data[ySize+cSize : ySize+2*cSize],
		YStride:        width,
		CStride:        width / 2,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, width, height),
	}, nil
}
