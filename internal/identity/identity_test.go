package identity

import "testing"

func TestNewUnenrolled(t *testing.T) {
	id := New("")
	if id.Status != StatusUnenrolled {
		t.Errorf("Status = %q, want %q", id.Status, StatusUnenrolled)
	}
	if id.IsEnrolled() {
		t.Error("IsEnrolled() = true, want false")
	}
	if id.EdgeID != "" {
		t.Errorf("EdgeID = %q, want empty", id.EdgeID)
	}
}

func TestNewEnrolled(t *testing.T) {
	id := New("edge-abc")
	if id.Status != StatusEnrolled {
		t.Errorf("Status = %q, want %q", id.Status, StatusEnrolled)
	}
	if !id.IsEnrolled() {
		t.Error("IsEnrolled() = false, want true")
	}
	if id.EdgeID != "edge-abc" {
		t.Errorf("EdgeID = %q, want %q", id.EdgeID, "edge-abc")
	}
}
