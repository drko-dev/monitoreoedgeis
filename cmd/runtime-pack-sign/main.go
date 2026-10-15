// Command runtime-pack-sign writes a release runtime-manifest.json that
// declares every regular file of a Runtime Pack, signs it with an Ed25519
// publisher key and prints the linker flags that pin that trust into the
// Wails installer. The private key never leaves -key; only public
// fingerprints are printed.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

func main() {
	pack := flag.String("pack", "", "Runtime Pack directory (runtime-manifest.json is written into it)")
	out := flag.String("out", "", "directory for runtime-manifest.signed.json and trust-anchors.json (outside the pack)")
	keyPath := flag.String("key", "", "Ed25519 seed file (hex); created with 0600 if missing")
	keyID := flag.String("key-id", "geocam-lab", "publisher key ID")
	version := flag.String("runtime-version", "", "runtime version")
	ffmpegVersion := flag.String("ffmpeg-version", "", "bundled FFmpeg version")
	validity := flag.Duration("validity", 365*24*time.Hour, "signature validity")
	flag.Parse()
	if *pack == "" || *out == "" || *keyPath == "" || *version == "" || *ffmpegVersion == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*pack, *out, *keyPath, *keyID, *version, *ffmpegVersion, *validity); err != nil {
		fmt.Fprintln(os.Stderr, "runtime-pack-sign:", err)
		os.Exit(1)
	}
}

func run(pack, out, keyPath, keyID, version, ffmpegVersion string, validity time.Duration) error {
	priv, err := loadOrCreateKey(keyPath)
	if err != nil {
		return err
	}
	manifest, err := buildManifest(pack, version, ffmpegVersion)
	if err != nil {
		return err
	}
	if err := manifest.ValidateSchema(); err != nil {
		return err
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(pack, installer.DefaultRuntimePackManifestName), manifestBytes, 0o644); err != nil {
		return err
	}
	expires := time.Now().Add(validity)
	signed, err := installer.SignManifestEd25519(manifest, keyID, priv, installer.SignOptions{ExpiresAt: &expires})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := signed.SaveToFile(filepath.Join(out, "runtime-manifest.signed.json")); err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	trust, err := json.MarshalIndent([]*installer.TrustAnchor{installer.NewEd25519TrustAnchor(keyID, pub)}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "trust-anchors.json"), trust, 0o644); err != nil {
		return err
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	keyDigest := sha256.Sum256(pub)
	fmt.Printf("-X main.releaseRuntimeManifestSHA256=%s -X main.releaseRuntimeKeyID=%s -X main.releaseRuntimePublicKeySHA256=%s\n",
		hex.EncodeToString(manifestDigest[:]), keyID, hex.EncodeToString(keyDigest[:]))
	return nil
}

func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(seed)), 0o600); err != nil {
			return nil, err
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("key file must hold a hex Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

const (
	pythonPath  = "python/bin/python3.11"
	workerPath  = "worker/worker.py"
	backendPath = "worker/backend.py"
	ffmpegPath  = "ffmpeg/ffmpeg"
	ffprobePath = "ffmpeg/ffprobe"
)

func buildManifest(pack, version, ffmpegVersion string) (*installer.RuntimeManifest, error) {
	digests := map[string]installer.FileSpec{}
	err := filepath.WalkDir(pack, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(pack, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink not allowed in Runtime Pack: %s (copy with dereference)", rel)
		}
		if d.IsDir() || rel == installer.DefaultRuntimePackManifestName {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("special file in Runtime Pack: %s", rel)
		}
		sum, size, err := hashFile(path)
		if err != nil {
			return err
		}
		digests[rel] = installer.FileSpec{RelativePath: rel, SHA256: sum, SizeBytes: size}
		return nil
	})
	if err != nil {
		return nil, err
	}
	take := func(rel string) (installer.FileSpec, error) {
		f, ok := digests[rel]
		if !ok {
			return f, fmt.Errorf("required pack file missing: %s", rel)
		}
		delete(digests, rel)
		return f, nil
	}
	py, err := take(pythonPath)
	if err != nil {
		return nil, err
	}
	worker, err := take(workerPath)
	if err != nil {
		return nil, err
	}
	backend, err := take(backendPath)
	if err != nil {
		return nil, err
	}
	m := &installer.RuntimeManifest{
		SchemaVersion:  installer.CurrentSchemaVersion,
		RuntimeVersion: version,
		ManifestType:   installer.ManifestTypeRelease,
		Platform:       installer.PlatformDarwin,
		Architecture:   installer.ArchArm64,
		Python:         installer.PythonSpec{Version: "3.11.9", ABI: "cp311", RelativeBinaryPath: py.RelativePath, SHA256: py.SHA256, SizeBytes: py.SizeBytes},
		Worker: installer.WorkerSpec{
			Version: version, Entrypoint: worker.RelativePath, SHA256: worker.SHA256, SizeBytes: worker.SizeBytes,
			BackendRelativePath: backend.RelativePath, BackendSHA256: backend.SHA256, BackendSizeBytes: backend.SizeBytes,
		},
		Compatibility: installer.CompatibilitySpec{EdgeMinVersion: "1.0.0", ModelsPackMinVersion: "1.0.0"},
	}
	for _, name := range []string{ffmpegPath, ffprobePath} {
		f, err := take(name)
		if err != nil {
			return nil, err
		}
		m.Binaries = append(m.Binaries, installer.BinarySpec{Name: filepath.Base(name), RelativePath: f.RelativePath, Version: ffmpegVersion, SHA256: f.SHA256, SizeBytes: f.SizeBytes})
	}
	rels := make([]string, 0, len(digests))
	for rel := range digests {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		f := digests[rel]
		if strings.HasPrefix(rel, "wheelhouse/") && strings.HasSuffix(rel, ".whl") {
			// PEP 427: {name}-{version}-...whl
			parts := strings.SplitN(filepath.Base(rel), "-", 3)
			if len(parts) == 3 {
				m.Dependencies = append(m.Dependencies, installer.DependencySpec{Name: parts[0], Version: parts[1], Filename: filepath.Base(rel), RelativePath: rel, SHA256: f.SHA256, SizeBytes: f.SizeBytes})
				continue
			}
		}
		m.Files = append(m.Files, f)
	}
	return m, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
