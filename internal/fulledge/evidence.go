package fulledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

const (
	evidenceSubdir = "evidence"
	capturesSubdir = "captures"
	defaultQuality = 85
)

// eventUUIDPattern matches internal/identity.NewUUIDv4's output exactly.
// Evidence paths are built from event_uuid alone (never a raw candidate_key
// — see the package-level note in this file) specifically so this check is
// enough to guarantee no path-traversal/escape component ever reaches
// filepath.Join below.
var eventUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// EvidenceRef records the metadata of a persisted local evidence JPEG.
type EvidenceRef struct {
	Path         string    `json:"path"` // relative path, e.g. "evidence/captures/uuid.jpg"
	SHA256       string    `json:"sha256"`
	SizeBytes    int64     `json:"size_bytes"`
	CapturedAt   time.Time `json:"captured_at"`
	SavedAt      time.Time `json:"saved_at"`
	ErrorMessage string    `json:"error_message,omitempty"`
}

// EvidenceManager manages storage, validation, and atomic publication of event evidence JPEGs.
type EvidenceManager struct {
	mu          sync.RWMutex
	dataDir     string
	evidenceDir string
	limits      *LimitsManager
	quality     int
	logger      *slog.Logger
}

// NewEvidenceManager creates an EvidenceManager rooted under dataDir/evidence.
func NewEvidenceManager(dataDir string, limits *LimitsManager, quality int, logger *slog.Logger) (*EvidenceManager, error) {
	if quality <= 0 || quality > 100 {
		quality = defaultQuality
	}
	evDir := filepath.Join(dataDir, evidenceSubdir)
	if err := os.MkdirAll(evDir, dirPerm); err != nil {
		return nil, fmt.Errorf("fulledge: create evidence dir %s: %w", evDir, err)
	}

	return &EvidenceManager{
		dataDir:     dataDir,
		evidenceDir: evDir,
		limits:      limits,
		quality:     quality,
		logger:      logger,
	}, nil
}

// SaveJPEG atomically writes JPEG bytes to disk as evidence for eventUUID.
//
// The path is built from eventUUID alone — never a raw candidate_key
// component (a camera-supplied/derived string) — and eventUUID is validated
// against the exact UUIDv4 shape internal/identity.NewUUIDv4 produces before
// it ever reaches filepath.Join, so there is no path-traversal/filesystem-
// escape surface here regardless of what upstream calls this with.
func (m *EvidenceManager) SaveJPEG(eventUUID string, capturedAt time.Time, jpegBytes []byte) (*EvidenceRef, error) {
	if !eventUUIDPattern.MatchString(eventUUID) {
		return nil, fmt.Errorf("fulledge: invalid event_uuid %q", eventUUID)
	}
	if len(jpegBytes) == 0 {
		return nil, fmt.Errorf("fulledge: cannot save empty evidence jpeg")
	}

	// 1. Check disk capacity
	if m.limits != nil {
		ok, _, err := m.limits.CanWriteEvidence(m.dataDir)
		if !ok || err != nil {
			return nil, err
		}
	}

	// 2. Compute SHA256 checksum
	h := sha256.Sum256(jpegBytes)
	shaHex := hex.EncodeToString(h[:])

	// 3. Prepare target path: evidence/captures/<eventUUID>.jpg
	targetSubdir := filepath.Join(m.evidenceDir, capturesSubdir)
	if err := os.MkdirAll(targetSubdir, dirPerm); err != nil {
		return nil, fmt.Errorf("fulledge: create captures evidence dir %s: %w", targetSubdir, err)
	}

	fileName := eventUUID + ".jpg"
	finalAbsPath := filepath.Join(targetSubdir, fileName)

	// Guard against overwriting an existing evidence file.
	// If identical content already exists, return the existing reference idempotently.
	// If divergent content exists for the same eventUUID, return ErrEvidenceConflict.
	if existingData, err := os.ReadFile(finalAbsPath); err == nil {
		existingH := sha256.Sum256(existingData)
		existingSHA := hex.EncodeToString(existingH[:])
		if existingSHA == shaHex {
			relPath, err := filepath.Rel(m.dataDir, finalAbsPath)
			if err != nil {
				relPath = filepath.Join(evidenceSubdir, capturesSubdir, fileName)
			}
			return &EvidenceRef{
				Path:       relPath,
				SHA256:     shaHex,
				SizeBytes:  int64(len(existingData)),
				CapturedAt: capturedAt.UTC(),
				SavedAt:    time.Now().UTC(),
			}, nil
		}
		return nil, fmt.Errorf("%w: %s", ErrEvidenceConflict, finalAbsPath)
	}

	// 4. Atomic write via temp file in same directory + rename
	tmpFile, err := os.CreateTemp(targetSubdir, ".evidence-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("fulledge: create temp evidence file: %w", err)
	}
	tmpPath := tmpFile.Name()

	cleanTmp := true
	defer func() {
		if cleanTmp {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(jpegBytes); err != nil {
		return nil, fmt.Errorf("fulledge: write temp evidence file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		return nil, fmt.Errorf("fulledge: sync temp evidence file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("fulledge: close temp evidence file: %w", err)
	}

	if err := os.Chmod(tmpPath, filePerm); err != nil {
		return nil, fmt.Errorf("fulledge: chmod temp evidence file: %w", err)
	}

	if err := os.Rename(tmpPath, finalAbsPath); err != nil {
		return nil, fmt.Errorf("fulledge: atomic rename evidence %s -> %s: %w", tmpPath, finalAbsPath, err)
	}

	cleanTmp = false

	// Compute path relative to dataDir
	relPath, err := filepath.Rel(m.dataDir, finalAbsPath)
	if err != nil {
		relPath = filepath.Join(evidenceSubdir, capturesSubdir, fileName)
	}

	return &EvidenceRef{
		Path:       relPath,
		SHA256:     shaHex,
		SizeBytes:  int64(len(jpegBytes)),
		CapturedAt: capturedAt.UTC(),
		SavedAt:    time.Now().UTC(),
	}, nil
}

// SaveFrame encodes a raw YUV420p video frame to JPEG and saves it as evidence.
func (m *EvidenceManager) SaveFrame(eventUUID string, f processing.Frame) (*EvidenceRef, error) {
	if len(f.Data) == 0 {
		return nil, fmt.Errorf("fulledge: empty frame data")
	}

	// Reuses internal/processing.YUV420PToImage (Milestone K1-K4) rather than
	// a second copy of the same yuv420p->image.YCbCr wrapper internal/
	// cloudsink and internal/vision already share.
	img, err := processing.YUV420PToImage(f.Data, f.OutputWidth, f.OutputHeight)
	if err != nil {
		return nil, fmt.Errorf("fulledge: wrap yuv420p: %w", err)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: m.quality}); err != nil {
		return nil, fmt.Errorf("fulledge: encode jpeg: %w", err)
	}

	return m.SaveJPEG(eventUUID, f.Timestamp, buf.Bytes())
}
