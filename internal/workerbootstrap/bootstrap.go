// Package workerbootstrap owns the single audited Python isolated-mode
// invocation contract shared by the Runtime Pack installer and Vision Worker.
package workerbootstrap

import "path/filepath"

// PythonSource is fixed executable code, never derived from a configuration
// value. Python -I intentionally omits the script directory from sys.path.
// The caller must authenticate and confine worker.py AND backend.py before use.
const PythonSource = "import runpy,sys; d=sys.argv.pop(1); p=sys.argv.pop(1); sys.path.insert(0,d); sys.argv[0]=p; runpy.run_path(p,run_name='__main__')"

// Args returns isolated Python arguments for a verified absolute entrypoint.
// Additional worker CLI flags are appended by the Go worker supervisor.
func Args(entrypoint string) []string {
	return []string{"-I", "-B", "-c", PythonSource, filepath.Dir(entrypoint), entrypoint}
}

// Entrypoint validates the exact auditable bootstrap protocol. It intentionally
// rejects arbitrary -c programs, extra environment-derived switches and
// independently chosen import directories.
func Entrypoint(args []string) (string, bool) {
	if len(args) != 6 || args[0] != "-I" || args[1] != "-B" ||
		args[2] != "-c" || args[3] != PythonSource ||
		!filepath.IsAbs(args[4]) || !filepath.IsAbs(args[5]) ||
		args[4] != filepath.Dir(args[5]) {
		return "", false
	}
	return args[5], true
}
