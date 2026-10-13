package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

func TestStructuredErrorEncodesSafeError(t *testing.T) {
	err := structuredError(&installer.SafeError{Code: "IDENTITY_ERROR", SafeMessage: "Failed to initialize device identity."})
	var got installer.SafeError
	if jsonErr := json.Unmarshal([]byte(err.Error()), &got); jsonErr != nil {
		t.Fatalf("error is not JSON: %q", err.Error())
	}
	if got.Code != "IDENTITY_ERROR" || got.SafeMessage == "" {
		t.Errorf("decoded = %+v", got)
	}
	if structuredError(nil) != nil {
		t.Error("nil error must stay nil")
	}
	plain := errors.New("boom")
	if structuredError(plain) != plain {
		t.Error("non-SafeError must pass through unchanged")
	}
}
