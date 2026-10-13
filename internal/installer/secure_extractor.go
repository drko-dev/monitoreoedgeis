package installer

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Standard typed error codes for secure extraction.
const (
	ErrCodeArchiveReadError        = "ARCHIVE_READ_ERROR"
	ErrCodeArchiveHashMismatch     = "ARCHIVE_HASH_MISMATCH"
	ErrCodeSecurityAbsolutePath    = "SECURITY_ABSOLUTE_PATH"
	ErrCodeSecurityTraversal       = "SECURITY_TRAVERSAL"
	ErrCodeSecurityEscapingPath    = "SECURITY_ESCAPING_PATH"
	ErrCodeSecurityEscapingSymlink = "SECURITY_ESCAPING_SYMLINK"
	ErrCodeSecurityAbsoluteSymlink = "SECURITY_ABSOLUTE_SYMLINK"
	ErrCodeSecurityHardlinkInvalid = "SECURITY_HARDLINK_INVALID"
	ErrCodeSecurityUnexpectedType  = "SECURITY_UNEXPECTED_FILETYPE"
	ErrCodeStagingMoveFailed       = "STAGING_MOVE_FAILED"
)

// ExtractOptions configures safe tar extraction constraints.
type ExtractOptions struct {
	MaxFileSize   int64 // Maximum size for any single extracted file (0 = default 1GB)
	MaxTotalBytes int64 // Maximum total bytes extracted (0 = default 2GB)
	MaxEntries    int   // Maximum number of entries (0 = default 50,000)
}

// DefaultExtractOptions provides conservative defaults preventing tar bomb attacks.
func DefaultExtractOptions() ExtractOptions {
	return ExtractOptions{
		MaxFileSize:   1024 * 1024 * 1024,     // 1 GB per file
		MaxTotalBytes: 2 * 1024 * 1024 * 1024, // 2 GB total
		MaxEntries:    50000,
	}
}

// ExtractSecureArchive extracts a .tar.gz archive into destinationDir with strict
// pre-extraction cryptographic verification and path confinement enforcement.
//
// Security guarantees:
//  1. Checksum verification occurs BEFORE opening/decompressing the archive.
//  2. Header path validation occurs BEFORE creating any filesystem entries.
//  3. Rejects absolute paths and path traversal ('..') segments.
//  4. Rejects symlinks pointing outside the destination directory.
//  5. Rejects hardlinks pointing outside the destination or to unextracted files.
//  6. Rejects block devices, character devices, FIFOs, and unknown types.
//  7. Refuses to write through existing symlinks.
//  8. Strips setuid and setgid permission bits.
func ExtractSecureArchive(archivePath, destinationDir, expectedSHA256 string) error {
	cleanArchive := filepath.Clean(archivePath)
	f, err := os.Open(cleanArchive)
	if err != nil {
		return &SafeError{
			Code:        ErrCodeArchiveReadError,
			SafeMessage: fmt.Sprintf("Error al abrir archivo de runtime %s: %v", cleanArchive, err),
			Recoverable: false,
			Err:         err,
		}
	}
	defer f.Close()

	if expectedSHA256 != "" {
		hash, err := hashFileSHA256(cleanArchive)
		if err != nil {
			return &SafeError{
				Code:        ErrCodeArchiveReadError,
				SafeMessage: fmt.Sprintf("Error al calcular hash de %s: %v", cleanArchive, err),
				Recoverable: false,
				Err:         err,
			}
		}
		if !strings.EqualFold(hash, expectedSHA256) {
			return &SafeError{
				Code: ErrCodeArchiveHashMismatch,
				SafeMessage: fmt.Sprintf("%v: checksum SHA-256 de archivo no coincide (esperado %s, calculado %s)",
					ErrArtifactCorrupted, expectedSHA256, hash),
				Recoverable: false,
				Err:         ErrArtifactCorrupted,
			}
		}
	}

	return ExtractTarGzSecurely(f, destinationDir, DefaultExtractOptions())
}

// ExtractTarGzSecurely decompresses a gzipped tar stream and extracts it into destinationDir.
func ExtractTarGzSecurely(r io.Reader, destinationDir string, opts ExtractOptions) error {
	gzReader, err := gzip.NewReader(r)
	if err != nil {
		return &SafeError{
			Code:        ErrCodeArchiveReadError,
			SafeMessage: fmt.Sprintf("Error al descompimir archivo gzip: %v", err),
			Recoverable: false,
			Err:         err,
		}
	}
	defer gzReader.Close()

	return ExtractTarSecurely(gzReader, destinationDir, opts)
}

// ExtractTarSecurely extracts an uncompressed tar stream into destinationDir with strict validation.
func ExtractTarSecurely(r io.Reader, destinationDir string, opts ExtractOptions) error {
	if opts.MaxFileSize == 0 {
		opts.MaxFileSize = 1024 * 1024 * 1024
	}
	if opts.MaxTotalBytes == 0 {
		opts.MaxTotalBytes = 2 * 1024 * 1024 * 1024
	}
	if opts.MaxEntries == 0 {
		opts.MaxEntries = 50000
	}

	absDest, err := filepath.Abs(filepath.Clean(destinationDir))
	if err != nil {
		return &SafeError{
			Code:        "INVALID_DEST_DIR",
			SafeMessage: fmt.Sprintf("Ruta de destino inválida: %s", destinationDir),
			Recoverable: false,
			Err:         err,
		}
	}

	if err := os.MkdirAll(absDest, 0o755); err != nil {
		return &SafeError{
			Code:        "DEST_MKDIR_FAILED",
			SafeMessage: fmt.Sprintf("Error al crear directorio destino %s: %v", absDest, err),
			Recoverable: true,
			Err:         err,
		}
	}

	canonicalDest, err := filepath.EvalSymlinks(absDest)
	if err != nil {
		return &SafeError{
			Code:        "DEST_EVAL_ERROR",
			SafeMessage: fmt.Sprintf("Error al resolver directorio destino %s: %v", absDest, err),
			Recoverable: false,
			Err:         err,
		}
	}
	canonicalDest = filepath.Clean(canonicalDest)

	tarReader := tar.NewReader(r)
	var totalBytes int64
	entriesCount := 0

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return &SafeError{
				Code:        ErrCodeArchiveReadError,
				SafeMessage: fmt.Sprintf("Error al leer entrada tar: %v", err),
				Recoverable: false,
				Err:         err,
			}
		}

		entriesCount++
		if entriesCount > opts.MaxEntries {
			return &SafeError{
				Code:        ErrCodeSecurityTraversal,
				SafeMessage: fmt.Sprintf("archivo tar excede el límite máximo de entradas (%d)", opts.MaxEntries),
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}

		rawName := header.Name
		if strings.TrimSpace(rawName) == "" {
			return &SafeError{
				Code:        ErrCodeSecurityTraversal,
				SafeMessage: "entrada de archivo tar con nombre vacío",
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}

		// 1. Rejection of absolute paths
		if filepath.IsAbs(rawName) || strings.HasPrefix(rawName, "/") || strings.HasPrefix(rawName, "\\") {
			return &SafeError{
				Code:        ErrCodeSecurityAbsolutePath,
				SafeMessage: fmt.Sprintf("%v: ruta absoluta en entrada tar no permitida (%s)", ErrArtifactSecurity, rawName),
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}

		// 2. Normalization and traversal checks
		cleanRel := filepath.Clean(filepath.ToSlash(rawName))
		if cleanRel == "." {
			continue // Root marker, safe to skip
		}
		if cleanRel == ".." || strings.HasPrefix(cleanRel, "../") || strings.Contains(cleanRel, "/../") {
			return &SafeError{
				Code:        ErrCodeSecurityTraversal,
				SafeMessage: fmt.Sprintf("%v: path traversal detectado en entrada tar (%s)", ErrArtifactSecurity, rawName),
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}

		targetPath := filepath.Join(canonicalDest, filepath.FromSlash(cleanRel))

		// 3. Confinement check
		confined, err := isPathConfined(canonicalDest, targetPath)
		if err != nil || !confined {
			return &SafeError{
				Code:        ErrCodeSecurityEscapingPath,
				SafeMessage: fmt.Sprintf("%v: ruta tar escapa del destino (%s)", ErrArtifactSecurity, rawName),
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}

		// 4. Component-by-component intermediate symlink validation
		relFromRoot, _ := filepath.Rel(canonicalDest, targetPath)
		parts := strings.FieldsFunc(relFromRoot, func(r rune) bool {
			return r == '/' || r == '\\'
		})
		currDir := canonicalDest
		for i := 0; i < len(parts)-1; i++ {
			currDir = filepath.Join(currDir, parts[i])
			lstat, err := os.Lstat(currDir)
			if err == nil && (lstat.Mode()&os.ModeSymlink != 0) {
				evalTarget, err := filepath.EvalSymlinks(currDir)
				if err != nil || !isPathConfinedQuick(canonicalDest, evalTarget) {
					return &SafeError{
						Code: ErrCodeSecurityEscapingSymlink,
						SafeMessage: fmt.Sprintf("%v: componente intermedio %s es un symlink que escapa del destino",
							ErrArtifactSecurity, currDir),
						Recoverable: false,
						Err:         ErrArtifactSecurity,
					}
				}
			}
		}

		// 5. Type-specific processing
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return &SafeError{
					Code:        "DIR_CREATE_FAILED",
					SafeMessage: fmt.Sprintf("Error al crear directorio %s: %v", targetPath, err),
					Recoverable: false,
					Err:         err,
				}
			}

		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > opts.MaxFileSize {
				return &SafeError{
					Code: ErrCodeSecurityTraversal,
					SafeMessage: fmt.Sprintf("archivo %s excede el tamaño máximo permitido (%d bytes)",
						cleanRel, opts.MaxFileSize),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			totalBytes += header.Size
			if totalBytes > opts.MaxTotalBytes {
				return &SafeError{
					Code: ErrCodeSecurityTraversal,
					SafeMessage: fmt.Sprintf("extracción excede el límite total permitido (%d bytes)",
						opts.MaxTotalBytes),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			parentDir := filepath.Dir(targetPath)
			if err := os.MkdirAll(parentDir, 0o755); err != nil {
				return &SafeError{
					Code:        "DIR_CREATE_FAILED",
					SafeMessage: fmt.Sprintf("Error al crear directorio padre %s: %v", parentDir, err),
					Recoverable: false,
					Err:         err,
				}
			}

			// Refuse to write through an existing symlink
			if lstat, err := os.Lstat(targetPath); err == nil && (lstat.Mode()&os.ModeSymlink != 0) {
				_ = os.Remove(targetPath)
			}

			// Strip setuid and setgid bits
			perm := header.FileInfo().Mode().Perm() & 0o777

			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return &SafeError{
					Code:        "FILE_CREATE_FAILED",
					SafeMessage: fmt.Sprintf("Error al crear archivo %s: %v", targetPath, err),
					Recoverable: false,
					Err:         err,
				}
			}

			written, err := io.Copy(outFile, io.LimitReader(tarReader, opts.MaxFileSize))
			closeErr := outFile.Close()
			if err != nil {
				_ = os.Remove(targetPath)
				return &SafeError{
					Code:        ErrCodeArchiveReadError,
					SafeMessage: fmt.Sprintf("Error al escribir archivo %s: %v", targetPath, err),
					Recoverable: false,
					Err:         err,
				}
			}
			if closeErr != nil {
				_ = os.Remove(targetPath)
				return &SafeError{
					Code:        "FILE_CLOSE_FAILED",
					SafeMessage: fmt.Sprintf("Error al cerrar archivo %s: %v", targetPath, closeErr),
					Recoverable: false,
					Err:         closeErr,
				}
			}
			if written != header.Size {
				_ = os.Remove(targetPath)
				return &SafeError{
					Code: ErrCodeArchiveReadError,
					SafeMessage: fmt.Sprintf("%v: tamaño escrito no coincide con cabecera tar (%d vs %d)",
						ErrArtifactCorrupted, written, header.Size),
					Recoverable: false,
					Err:         ErrArtifactCorrupted,
				}
			}

		case tar.TypeSymlink:
			linkTarget := header.Linkname
			if strings.TrimSpace(linkTarget) == "" {
				return &SafeError{
					Code:        ErrCodeSecurityEscapingSymlink,
					SafeMessage: fmt.Sprintf("symlink %s tiene destino vacío", rawName),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			if filepath.IsAbs(linkTarget) || strings.HasPrefix(linkTarget, "/") || strings.HasPrefix(linkTarget, "\\") {
				return &SafeError{
					Code: ErrCodeSecurityAbsoluteSymlink,
					SafeMessage: fmt.Sprintf("%v: symlink con destino absoluto no permitido (%s -> %s)",
						ErrArtifactSecurity, rawName, linkTarget),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			symlinkDir := filepath.Dir(targetPath)
			if err := os.MkdirAll(symlinkDir, 0o755); err != nil {
				return &SafeError{
					Code:        "DIR_CREATE_FAILED",
					SafeMessage: fmt.Sprintf("Error al crear directorio %s: %v", symlinkDir, err),
					Recoverable: false,
					Err:         err,
				}
			}

			// Verify target does not escape destination directory
			cleanLinkTarget := filepath.Clean(filepath.ToSlash(linkTarget))
			resolvedTarget := filepath.Clean(filepath.Join(symlinkDir, filepath.FromSlash(cleanLinkTarget)))
			confined, err := isPathConfined(canonicalDest, resolvedTarget)
			if err != nil || !confined {
				return &SafeError{
					Code: ErrCodeSecurityEscapingSymlink,
					SafeMessage: fmt.Sprintf("%v: symlink apunta fuera del destino (%s -> %s)",
						ErrArtifactSecurity, rawName, linkTarget),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			// Remove existing file or symlink before recreating
			_ = os.Remove(targetPath)
			if err := os.Symlink(linkTarget, targetPath); err != nil {
				return &SafeError{
					Code:        "SYMLINK_CREATE_FAILED",
					SafeMessage: fmt.Sprintf("Error al crear symlink %s -> %s: %v", targetPath, linkTarget, err),
					Recoverable: false,
					Err:         err,
				}
			}

		case tar.TypeLink:
			hardTarget := header.Linkname
			if strings.TrimSpace(hardTarget) == "" {
				return &SafeError{
					Code:        ErrCodeSecurityHardlinkInvalid,
					SafeMessage: fmt.Sprintf("hardlink %s tiene destino vacío", rawName),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			if filepath.IsAbs(hardTarget) || strings.HasPrefix(hardTarget, "/") || strings.HasPrefix(hardTarget, "\\") {
				return &SafeError{
					Code: ErrCodeSecurityHardlinkInvalid,
					SafeMessage: fmt.Sprintf("%v: hardlink con destino absoluto no permitido (%s -> %s)",
						ErrArtifactSecurity, rawName, hardTarget),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			cleanHardTarget := filepath.Clean(filepath.ToSlash(hardTarget))
			oldPath := filepath.Join(canonicalDest, filepath.FromSlash(cleanHardTarget))
			confined, err := isPathConfined(canonicalDest, oldPath)
			if err != nil || !confined {
				return &SafeError{
					Code: ErrCodeSecurityHardlinkInvalid,
					SafeMessage: fmt.Sprintf("%v: hardlink apunta fuera del destino (%s -> %s)",
						ErrArtifactSecurity, rawName, hardTarget),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			// Target must already exist inside destination as a regular file
			oldInfo, err := os.Lstat(oldPath)
			if err != nil || !oldInfo.Mode().IsRegular() {
				return &SafeError{
					Code: ErrCodeSecurityHardlinkInvalid,
					SafeMessage: fmt.Sprintf("%v: hardlink hace referencia a archivo inexistente o no regular (%s)",
						ErrArtifactSecurity, hardTarget),
					Recoverable: false,
					Err:         ErrArtifactSecurity,
				}
			}

			parentDir := filepath.Dir(targetPath)
			if err := os.MkdirAll(parentDir, 0o755); err != nil {
				return &SafeError{
					Code:        "DIR_CREATE_FAILED",
					SafeMessage: fmt.Sprintf("Error al crear directorio %s: %v", parentDir, err),
					Recoverable: false,
					Err:         err,
				}
			}

			_ = os.Remove(targetPath)
			if err := os.Link(oldPath, targetPath); err != nil {
				return &SafeError{
					Code:        "HARDLINK_CREATE_FAILED",
					SafeMessage: fmt.Sprintf("Error al crear hardlink %s -> %s: %v", targetPath, oldPath, err),
					Recoverable: false,
					Err:         err,
				}
			}

		default:
			return &SafeError{
				Code: ErrCodeSecurityUnexpectedType,
				SafeMessage: fmt.Sprintf("%v: tipo de archivo tar no permitido (%c en %s)",
					ErrArtifactSecurity, header.Typeflag, rawName),
				Recoverable: false,
				Err:         ErrArtifactSecurity,
			}
		}
	}

	return nil
}

// isPathConfinedQuick performs quick confinement check between baseDir and targetPath.
func isPathConfinedQuick(baseDir, targetPath string) bool {
	confined, err := isPathConfined(baseDir, targetPath)
	return err == nil && confined
}

// ExtractAndStageArchive implements atomic extraction and staging.
//
// Staging & Atomic Activation Protocol:
//  1. Creates temporary staging directory as a hidden sibling of targetDir on the exact SAME filesystem.
//  2. Extracts archive securely into the staging directory.
//  3. Executes validateFn on the staged directory. If validation fails, staging is deleted and existing targetDir is NEVER modified.
//  4. If targetDir already exists, moves targetDir to backup, swaps stagingDir to targetDir atomically, and removes backup on success.
//  5. If activation swap fails, automatically rolls back targetDir from backup.
func ExtractAndStageArchive(
	archivePath string,
	targetDir string,
	expectedSHA256 string,
	validateFn func(stagedDir string) error,
) error {
	absTarget, err := filepath.Abs(filepath.Clean(targetDir))
	if err != nil {
		return err
	}
	parentDir := filepath.Dir(absTarget)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return err
	}

	// Staging directory created as sibling in the same parent directory -> guaranteed SAME filesystem
	stagingDir := filepath.Join(parentDir, fmt.Sprintf(".%s.tmp.%d", filepath.Base(absTarget), time.Now().UnixNano()))
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return err
	}

	cleanStaging := true
	defer func() {
		if cleanStaging {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	// Extract into staging
	if err := ExtractSecureArchive(archivePath, stagingDir, expectedSHA256); err != nil {
		return err
	}

	// Fully validate staging BEFORE touching targetDir
	if validateFn != nil {
		if err := validateFn(stagingDir); err != nil {
			return err
		}
	}

	// Atomic activation on the same filesystem
	if _, err := os.Stat(absTarget); os.IsNotExist(err) {
		cleanStaging = false
		if err := os.Rename(stagingDir, absTarget); err != nil {
			cleanStaging = true
			return &SafeError{
				Code:        ErrCodeStagingMoveFailed,
				SafeMessage: fmt.Sprintf("Error al activar staging hacia %s: %v", absTarget, err),
				Recoverable: true,
				Err:         err,
			}
		}
		return nil
	}

	// Existing targetDir present: preserve in backup, swap, rollback on failure
	backupDir := filepath.Join(parentDir, fmt.Sprintf(".%s.old.%d", filepath.Base(absTarget), time.Now().UnixNano()))
	if err := os.Rename(absTarget, backupDir); err != nil {
		return &SafeError{
			Code:        ErrCodeStagingMoveFailed,
			SafeMessage: fmt.Sprintf("Error al respaldar runtime previo: %v", err),
			Recoverable: true,
			Err:         err,
		}
	}

	cleanStaging = false
	if err := os.Rename(stagingDir, absTarget); err != nil {
		cleanStaging = true
		// Rollback previous runtime!
		_ = os.Rename(backupDir, absTarget)
		return &SafeError{
			Code:        ErrCodeStagingMoveFailed,
			SafeMessage: fmt.Sprintf("Fallo al activar nuevo runtime, runtime previo restaurado: %v", err),
			Recoverable: true,
			Err:         err,
		}
	}

	// Verification and activation complete: clean up backup
	_ = os.RemoveAll(backupDir)
	return nil
}
