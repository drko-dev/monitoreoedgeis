package fulledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

const (
	evidenceSubdir = "evidence"
	defaultQuality = 85
)

// EvidenceRef records the metadata of a persisted local evidence JPEG.
type EvidenceRef struct {
	Path         string    `json:"path"` // relative path, e.g. "evidence/cam-1/uuid.jpg"
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
func (m *EvidenceManager) SaveJPEG(eventUUID, candidateKey string, capturedAt time.Time, jpegBytes []byte) (*EvidenceRef, error) {
	if eventUUID == "" {
		return nil, fmt.Errorf("fulledge: cannot save evidence without event_uuid")
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

	// 3. Prepare target path: evidence/<candidateKey>/<eventUUID>.jpg
	targetSubdir := filepath.Join(m.evidenceDir, candidateKey)
	if err := os.MkdirAll(targetSubdir, dirPerm); err != nil {
		return nil, fmt.Errorf("fulledge: create candidate evidence dir %s: %w", targetSubdir, err)
	}

	fileName := eventUUID + ".jpg"
	finalAbsPath := filepath.Join(targetSubdir, fileName)

	// Guard against overwriting an existing evidence file
	if _, err := os.Stat(finalAbsPath); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrEvidenceAlreadyExists, finalAbsPath)
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
		relPath = filepath.Join(evidenceSubdir, candidateKey, fileName)
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

	img, err := yuv420pToImage(f.Data, f.OutputWidth, f.OutputHeight)
	if err != nil {
		return nil, fmt.Errorf("fulledge: wrap yuv420p: %w", err)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: m.quality}); err != nil {
		return nil, fmt.Errorf("fulledge: encode jpeg: %w", err)
	}

	return m.SaveJPEG(eventUUID, f.CandidateKey, f.Timestamp, buf.Bytes())
}

// yuv420pToImage wraps raw YUV420p planar data into *image.YCbCr for direct jpeg encoding.
func yuv420pToImage(data []byte, width, height int) (*image.YCbCr, error) {
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
