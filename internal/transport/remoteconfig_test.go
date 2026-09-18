package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestGetDesiredConfig_NoContent(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != RemoteConfigNextPath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	c, err := New(srv.URL, true, 2*time.Second, "test")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := c.GetDesiredConfig(context.Background(), "dev-1", "cred-1")
	if err != nil {
		t.Fatalf("GetDesiredConfig() error = %v", err)
	}
	if cfg != nil {
		t.Fatalf("cfg = %+v, want nil", cfg)
	}
}

func TestGetDesiredConfig_Success(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config": map[string]any{
				"version": 3,
				"payload": map[string]any{"fps": 10},
			},
		})
	})

	c, err := New(srv.URL, true, 2*time.Second, "test")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := c.GetDesiredConfig(context.Background(), "dev-1", "cred-1")
	if err != nil {
		t.Fatalf("GetDesiredConfig() error = %v", err)
	}
	if cfg == nil || cfg.Version != 3 {
		t.Fatalf("unexpected cfg = %+v", cfg)
	}
}

func TestAckRemoteConfig_SuccessAndUnauthorized(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != RemoteConfigAckPath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})
	c, _ := New(srv.URL, true, 2*time.Second, "test")
	if err := c.AckRemoteConfig(context.Background(), "dev-1", "cred-1", 3, "applied", ""); err != nil {
		t.Fatalf("AckRemoteConfig() error = %v", err)
	}

	srv2 := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c2, _ := New(srv2.URL, true, 2*time.Second, "test")
	err := c2.AckRemoteConfig(context.Background(), "dev-1", "cred-1", 3, "failed", "APPLY_FAILED")
	if err == nil {
		t.Fatal("expected an error for unauthorized ack")
	}
}
