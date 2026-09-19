package ota

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in      string
		want    Version
		wantErr bool
	}{
		{"v1.2.3", Version{1, 2, 3}, false},
		{"1.2.3", Version{1, 2, 3}, false},
		{"v0.0.0", Version{0, 0, 0}, false},
		{"v1.2", Version{}, true},
		{"v1.2.3.4", Version{}, true},
		{"v1.2.x", Version{}, true},
		{"v01.2.3", Version{}, true},
		{"v1.2.-3", Version{}, true},
		{"", Version{}, true},
		{"vlatest", Version{}, true},
	}
	for _, tc := range cases {
		got, err := ParseVersion(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseVersion(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseVersion(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseVersion(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestIsUpdateEligible(t *testing.T) {
	cases := []struct {
		current, candidate string
		want               bool
		wantErr            bool
	}{
		{"v1.0.0", "v1.0.1", true, false},
		{"v1.0.0", "v2.0.0", true, false},
		{"v1.0.0", "v1.0.0", false, false}, // equal is not eligible
		{"v1.0.1", "v1.0.0", false, false}, // downgrade is not eligible
		{"0.1.0", "v0.2.0", true, false},   // dev default form vs canonical
		{"v1.0.0", "not-a-version", false, true},
		{"garbage", "v1.0.0", false, true},
	}
	for _, tc := range cases {
		got, err := IsUpdateEligible(tc.current, tc.candidate)
		if tc.wantErr {
			if err == nil {
				t.Errorf("IsUpdateEligible(%q, %q) = %v, want error", tc.current, tc.candidate, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("IsUpdateEligible(%q, %q) unexpected error: %v", tc.current, tc.candidate, err)
			continue
		}
		if got != tc.want {
			t.Errorf("IsUpdateEligible(%q, %q) = %v, want %v", tc.current, tc.candidate, got, tc.want)
		}
	}
}
