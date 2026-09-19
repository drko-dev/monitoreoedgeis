package ota

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"testing"
)

// buildApplianceTarball writes a minimal .tar.gz at path with one entry per
// (name -> content) pair in files, mirroring the layout
// deploy/appliance/scripts/package.sh produces (or a deliberately reduced
// subset, for the "missing entries" test).
func buildApplianceTarball(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write tar content %s: %v", name, err)
		}
	}
}
