package workerbootstrap

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolatedWorkerBootstrapContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker", "worker.py")
	args := Args(path)
	if got, ok := Entrypoint(args); !ok || got != path {
		t.Fatalf("valid bootstrap not recognized: %v", args)
	}
	for _, tc := range []struct {
		name   string
		change func([]string) []string
	}{
		{"inject arbitrary Python", func(a []string) []string { a[3] = "import os; os.system('true')"; return a }},
		{"external import path", func(a []string) []string { a[4] = t.TempDir(); return a }},
		{"disable isolated mode", func(a []string) []string { a[0] = "-E"; return a }},
		{"relative worker", func(a []string) []string { a[5] = "worker.py"; return a }},
		{"extra command switch", func(a []string) []string { return append(a, "-m") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := tc.change(Args(path))
			if _, ok := Entrypoint(mutated); ok {
				t.Fatalf("untrusted bootstrap accepted: %q", strings.Join(mutated, " "))
			}
		})
	}
}
