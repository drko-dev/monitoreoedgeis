package vision

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// ModelStatus is one required model weight file's on-disk state, published
// verbatim under /status. Never includes an absolute host filesystem layout
// beyond the configured models directory — no other sensitive paths.
type ModelStatus struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Present   bool   `json:"present"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	// SHA256 is populated lazily (Checksum), never computed on every Status
	// call — these files are tens of MB and Status must stay cheap.
	SHA256    string `json:"sha256,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// ModelManager tracks the two YOLO weight files K3 requires
// (GEOCAM_EDGE_YOLO_PERSON_MODEL / _VEHICLE_MODEL, default
// yolo11s-pose.pt/yolo11n.pt) inside a directory kept outside any versioned
// release tree (see docs/deployment/appliance.md), so an agent update never
// destroys installed weights.
//
// ModelManager never downloads a model: an operator or a separate,
// explicit provisioning step (dev-only for now — signed OTA is Hito T's job,
// not this one's) must place the files. A missing file is reported as a
// clear, non-fatal status (Ready()==false), never a silent auto-fetch.
type ModelManager struct {
	dir          string
	personModel  string
	vehicleModel string

	mu        sync.Mutex
	checksums map[string]string // name -> sha256, cached by (size,modtime) key below
	cacheKey  map[string]string
}

// NewModelManager creates a manager rooted at dir, expecting exactly
// personModel and vehicleModel inside it.
func NewModelManager(dir, personModel, vehicleModel string) *ModelManager {
	return &ModelManager{
		dir:          dir,
		personModel:  personModel,
		vehicleModel: vehicleModel,
		checksums:    make(map[string]string),
		cacheKey:     make(map[string]string),
	}
}

// Status stats both required model files. It never reads file contents (no
// checksum) — see Checksum for that, on demand.
func (m *ModelManager) Status() []ModelStatus {
	names := []string{m.personModel, m.vehicleModel}
	out := make([]ModelStatus, 0, len(names))
	for _, name := range names {
		out = append(out, m.statOne(name))
	}
	return out
}

func (m *ModelManager) statOne(name string) ModelStatus {
	path := filepath.Join(m.dir, name)
	st := ModelStatus{Name: name, Path: path}
	info, err := os.Stat(path)
	switch {
	case err == nil && info.Mode().IsRegular():
		st.Present = true
		st.SizeBytes = info.Size()
	case err == nil:
		st.LastError = "not a regular file"
	case os.IsNotExist(err):
		st.LastError = "model_missing"
	default:
		st.LastError = err.Error()
	}
	return st
}

// Ready reports whether both required model files are present as regular
// files. It does not verify they load correctly — the worker (K2) reports
// that once it actually tries.
func (m *ModelManager) Ready() bool {
	for _, s := range m.Status() {
		if !s.Present {
			return false
		}
	}
	return true
}

// PersonModelPath and VehicleModelPath are absolute paths passed to the
// worker subprocess.
func (m *ModelManager) PersonModelPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return filepath.Join(m.dir, m.personModel)
}

func (m *ModelManager) VehicleModelPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return filepath.Join(m.dir, m.vehicleModel)
}

// SetModels updates the active person and vehicle model names.
func (m *ModelManager) SetModels(person, vehicle string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if person != "" {
		m.personModel = person
	}
	if vehicle != "" {
		m.vehicleModel = vehicle
	}
}

// Models returns the active person and vehicle model names.
func (m *ModelManager) Models() (person, vehicle string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.personModel, m.vehicleModel
}

// Checksum returns the sha256 of the named model file, computed once and
// cached until the file's (size, mtime) pair changes. Returns an error if
// the file is missing or unreadable.
func (m *ModelManager) Checksum(name string) (string, error) {
	path := filepath.Join(m.dir, name)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	key := info.ModTime().String() + ":" + strconv.FormatInt(info.Size(), 10)

	m.mu.Lock()
	if m.cacheKey[name] == key {
		sum := m.checksums[name]
		m.mu.Unlock()
		return sum, nil
	}
	m.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))

	m.mu.Lock()
	m.checksums[name] = sum
	m.cacheKey[name] = key
	m.mu.Unlock()
	return sum, nil
}
