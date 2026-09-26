package transport

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPostANPRCandidate_Success_SendsRealMultipart(t *testing.T) {
	metadataJSON := []byte(`{"schema_version":"anpr_candidate_v1","candidate_id":"c1"}`)
	cropJPEG := []byte{0xFF, 0xD8, 0xFF, 0xD9, 0x01, 0x02}

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != AnprCandidatesPath {
			t.Errorf("path = %q, want %q", r.URL.Path, AnprCandidatesPath)
		}
		if got := r.Header.Get("X-Device-Id"); got != "device-1" {
			t.Errorf("X-Device-Id = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cred-1" {
			t.Errorf("Authorization = %q", got)
		}

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
			t.Fatalf("Content-Type = %q, want multipart/*: %v", r.Header.Get("Content-Type"), err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])

		var gotMeta []byte
		var gotCrop []byte
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("multipart NextPart: %v", err)
			}
			data, _ := io.ReadAll(part)
			switch part.FormName() {
			case "metadata":
				gotMeta = data
				if ct := part.Header.Get("Content-Type"); strings.Contains(ct, "image") {
					t.Errorf("metadata part had an image Content-Type: %q", ct)
				}
			case "crop":
				gotCrop = data
			default:
				t.Errorf("unexpected multipart field %q", part.FormName())
			}
		}
		if string(gotMeta) != string(metadataJSON) {
			t.Errorf("metadata part = %q, want %q", gotMeta, metadataJSON)
		}
		if string(gotCrop) != string(cropJPEG) {
			t.Errorf("crop part = %v, want %v", gotCrop, cropJPEG)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	c, err := New(srv.URL, true, 2*time.Second, "test")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := c.PostANPRCandidate(context.Background(), "device-1", "cred-1", metadataJSON, cropJPEG); err != nil {
		t.Fatalf("PostANPRCandidate() error = %v", err)
	}
}

func TestPostANPRCandidate_StatusClassification(t *testing.T) {
	cases := []struct {
		status  int
		wantErr error
	}{
		{http.StatusAccepted, nil},
		{http.StatusOK, nil},
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrUnauthorized},
		{http.StatusNotFound, ErrInvalidRequest},            // unknown camera_key
		{http.StatusUnprocessableEntity, ErrInvalidRequest}, // bad hash/size
		{http.StatusRequestTimeout, ErrRetryableStatus},
		{http.StatusInternalServerError, ErrRetryableStatus},
	}
	for _, tc := range cases {
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(tc.status)
		})
		c, err := New(srv.URL, true, 2*time.Second, "test")
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		err = c.PostANPRCandidate(context.Background(), "device-1", "cred-1", []byte(`{}`), []byte{0x01})
		if tc.wantErr == nil {
			if err != nil {
				t.Errorf("status %d: PostANPRCandidate() error = %v, want nil", tc.status, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("status %d: PostANPRCandidate() error = nil, want %v", tc.status, tc.wantErr)
			continue
		}
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("status %d: PostANPRCandidate() error = %v, want wrapping %v", tc.status, err, tc.wantErr)
		}
	}
}

func TestPostANPRCandidate_SaaSUnreachable(t *testing.T) {
	c, err := New("http://127.0.0.1:1", true, 200*time.Millisecond, "test") // nothing listens here
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = c.PostANPRCandidate(context.Background(), "device-1", "cred-1", []byte(`{}`), []byte{0x01})
	if err == nil {
		t.Fatal("expected an error when the SaaS is unreachable")
	}
}
