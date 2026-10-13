// Command runtime-extractor is a standalone utility for securely validating and extracting
// hermetic runtime pack archives without path traversal, escaping symlinks, or intermediate race conditions.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
)

func main() {
	archivePath := flag.String("archive", "", "Path to the compressed runtime archive (.tar.gz)")
	targetDir := flag.String("target", "", "Destination directory")
	expectedSHA := flag.String("expected-sha256", "", "Expected SHA-256 hex digest of the archive")
	stage := flag.Bool("stage", true, "Perform atomic same-filesystem staging with rollback protection")
	flag.Parse()

	if *archivePath == "" || *targetDir == "" {
		fmt.Fprintf(os.Stderr, "usage: runtime-extractor --archive <path> --target <dir> [--expected-sha256 <hash>] [--stage]\n")
		os.Exit(2)
	}

	var err error
	if *stage {
		err = installer.ExtractAndStageArchive(*archivePath, *targetDir, *expectedSHA, nil)
	} else {
		err = installer.ExtractSecureArchive(*archivePath, *targetDir, *expectedSHA)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "runtime-extractor error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("ok: successfully extracted %s into %s\n", *archivePath, *targetDir)
}
