package processing

import "fmt"

// ValidateOutputDimensions enforces the only two valid shapes for the
// resize config: both dimensions zero (resize disabled) or both positive
// and even (required for yuv420p's half-resolution chroma planes). Any
// other combination — one zero and one not, or an odd value — is rejected
// at config load time rather than surfacing as a runtime resize bug.
func ValidateOutputDimensions(width, height int) error {
	if width == 0 && height == 0 {
		return nil
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("processing: output width/height must both be zero or both be positive, got %dx%d", width, height)
	}
	if width%2 != 0 || height%2 != 0 {
		return fmt.Errorf("processing: output width/height must be even (yuv420p), got %dx%d", width, height)
	}
	return nil
}

// Resize converts frame to targetW x targetH using nearest-neighbor
// sampling on each yuv420p plane independently. A no-op (frame returned
// unchanged) when resize is disabled (targetW/H <= 0) or already matches
// the source size — the common case, since substream cameras are often
// already close to the desired output resolution.
func Resize(frame DecodedFrame, targetW, targetH int) DecodedFrame {
	if targetW <= 0 || targetH <= 0 || (targetW == frame.Width && targetH == frame.Height) {
		return frame
	}

	srcW, srcH := frame.Width, frame.Height
	srcCW, srcCH := srcW/2, srcH/2
	dstCW, dstCH := targetW/2, targetH/2

	ySize := srcW * srcH
	cSize := srcCW * srcCH
	srcY := frame.Data[:ySize]
	srcU := frame.Data[ySize : ySize+cSize]
	srcV := frame.Data[ySize+cSize : ySize+2*cSize]

	dstYSize := targetW * targetH
	dstCSize := dstCW * dstCH
	dst := make([]byte, dstYSize+2*dstCSize)
	dstY := dst[:dstYSize]
	dstU := dst[dstYSize : dstYSize+dstCSize]
	dstV := dst[dstYSize+dstCSize : dstYSize+2*dstCSize]

	resizePlane(srcY, srcW, srcH, dstY, targetW, targetH)
	resizePlane(srcU, srcCW, srcCH, dstU, dstCW, dstCH)
	resizePlane(srcV, srcCW, srcCH, dstV, dstCW, dstCH)

	out := frame
	out.Data = dst
	out.Width = targetW
	out.Height = targetH
	return out
}

func resizePlane(src []byte, srcW, srcH int, dst []byte, dstW, dstH int) {
	for y := 0; y < dstH; y++ {
		sy := y * srcH / dstH
		srow := sy * srcW
		drow := y * dstW
		for x := 0; x < dstW; x++ {
			sx := x * srcW / dstW
			dst[drow+x] = src[srow+sx]
		}
	}
}
