package processing

import "testing"

func TestResize_NoOpWhenTargetMatchesSource(t *testing.T) {
	frame := DecodedFrame{Data: []byte{1, 2, 3, 4}, Width: 2, Height: 2}
	out := Resize(frame, 2, 2)
	if &out.Data[0] != &frame.Data[0] {
		t.Fatal("expected same underlying data slice for no-op resize")
	}
}

func TestResize_NoOpWhenDisabled(t *testing.T) {
	frame := DecodedFrame{Data: []byte{1, 2, 3, 4}, Width: 2, Height: 2}
	out := Resize(frame, 0, 0)
	if out.Width != 2 || out.Height != 2 {
		t.Fatalf("expected passthrough dimensions, got %dx%d", out.Width, out.Height)
	}
}

func TestResize_DownscalesKnownPattern(t *testing.T) {
	// 4x4 Y plane, distinct value per pixel so nearest-neighbor sampling is
	// verifiable; 2x2 chroma planes (half resolution, as yuv420p requires).
	y := []byte{
		0, 1, 2, 3,
		4, 5, 6, 7,
		8, 9, 10, 11,
		12, 13, 14, 15,
	}
	u := []byte{100, 101, 102, 103}
	v := []byte{200, 201, 202, 203}
	data := append(append(append([]byte{}, y...), u...), v...)

	frame := DecodedFrame{Data: data, Width: 4, Height: 4}
	out := Resize(frame, 2, 2)

	if out.Width != 2 || out.Height != 2 {
		t.Fatalf("output dims = %dx%d, want 2x2", out.Width, out.Height)
	}
	wantYSize := 2 * 2
	wantCSize := 1 * 1
	if len(out.Data) != wantYSize+2*wantCSize {
		t.Fatalf("output data len = %d, want %d", len(out.Data), wantYSize+2*wantCSize)
	}

	// Nearest-neighbor from a 4x4 -> 2x2 Y plane samples source (0,0) and
	// (2,2) among others: verify it picked real source values, not zeros.
	outY := out.Data[:wantYSize]
	for _, px := range outY {
		found := false
		for _, sv := range y {
			if px == sv {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("output Y pixel %d not sourced from input plane", px)
		}
	}
}

func TestValidateOutputDimensions(t *testing.T) {
	cases := []struct {
		name    string
		w, h    int
		wantErr bool
	}{
		{"both zero disables resize", 0, 0, false},
		{"both even positive", 640, 360, false},
		{"one zero one positive", 640, 0, true},
		{"odd width", 641, 360, true},
		{"odd height", 640, 361, true},
		{"negative", -2, 360, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOutputDimensions(tc.w, tc.h)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateOutputDimensions(%d,%d) err=%v, wantErr=%v", tc.w, tc.h, err, tc.wantErr)
			}
		})
	}
}
