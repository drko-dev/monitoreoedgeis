package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// JournalState represents explicit transaction lifecycle states for crash recovery.
type JournalState string

const (
	JournalStateIdle        JournalState = "IDLE"
	JournalStatePreparing   JournalState = "PREPARING"
	JournalStateStaged      JournalState = "STAGED"
	JournalStateBackingUp   JournalState = "BACKING_UP"
	JournalStateBackedUp    JournalState = "BACKED_UP"
	JournalStateActivating  JournalState = "ACTIVATING"
	JournalStateActivated   JournalState = "ACTIVATED"
	JournalStateRollingBack JournalState = "ROLLING_BACK"
	JournalStateRolledBack  JournalState = "ROLLED_BACK"
	JournalStateCommitted   JournalState = "COMMITTED"
	JournalStateFailed      JournalState = "FAILED"
)

var (
	ErrRecoveryLockHeld         = errors.New("recovery manager lock already held by another process")
	ErrRecoveryLockNotHeld      = errors.New("recovery manager lock must be held for filesystem operations")
	ErrRecoveryAmbiguous        = errors.New("ambiguous recovery state: both active and backup exist with data")
	ErrJournalCorrupt           = errors.New("recovery journal corrupted or checksum mismatch")
	ErrSecurityEscapingSymlink  = errors.New("security violation: path escapes authorized root directory")
	ErrRollbackFailed           = errors.New("critical: rollback failed to restore backup")
	ErrTransactionInProgress    = errors.New("transaction already in progress")
	ErrTransactionNotInProgress = errors.New("no transaction in progress")
)

const (
	ErrCodeRecoveryLockHeld     = "RECOVERY_LOCK_HELD"
	ErrCodeRecoveryLockRequired = "RECOVERY_LOCK_REQUIRED"
	ErrCodeRecoveryAmbiguous    = "RECOVERY_AMBIGUOUS_BACKUP"
	ErrCodeJournalCorrupt       = "RECOVERY_JOURNAL_CORRUPT"
	ErrCodeRollbackFailed       = "RECOVERY_ROLLBACK_FAILED"
	ErrCodeRecoveryFailed       = "RECOVERY_FAILED"
)

// JournalRecord is the persisted, checksummed record of a recovery transaction.
type JournalRecord struct {
	Version      int          `json:"version"`
	TxID         string       `json:"tx_id"`
	State        JournalState `json:"state"`
	Target       string       `json:"target"`
	TargetVer    string       `json:"target_ver"`
	ActivePath   string       `json:"active_path"`   // Relative to baseDir
	StagingPath  string       `json:"staging_path"`  // Relative to baseDir
	BackupPath   string       `json:"backup_path"`   // Relative to baseDir
	ExpectedHash string       `json:"expected_hash"` // Caller metadata only; never runtime integrity evidence.
	// HadActive distinguishes an upgrade rollback (a prior runtime is required)
	// from an interrupted first install (there is no prior runtime to restore).
	HadActive      bool      `json:"had_active"`
	BackupHash     string    `json:"backup_tree_sha256,omitempty"`
	StagingHash    string    `json:"staging_tree_sha256,omitempty"`
	ActiveHash     string    `json:"active_tree_sha256,omitempty"`
	QuarantinePath string    `json:"quarantine_path,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
	RecordHash     string    `json:"record_hash"` // SHA-256 of payload fields
}

func (r *JournalRecord) computeHash() string {
	payload := fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%s|%s|%t|%s|%s|%s|%s|%d",
		r.Version,
		r.TxID,
		r.State,
		r.Target,
		r.TargetVer,
		r.ActivePath,
		r.StagingPath,
		r.BackupPath,
		r.ExpectedHash,
		r.HadActive,
		r.BackupHash,
		r.StagingHash,
		r.ActiveHash,
		r.QuarantinePath,
		r.Timestamp.UnixNano(),
	)
	h := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(h[:])
}

func (r *JournalRecord) isValid() bool {
	if r == nil || r.Version != 1 || r.TxID == "" || !validTargetName(r.Target) || r.Timestamp.IsZero() {
		return false
	}
	if r.Timestamp.UnixNano() <= 0 {
		return false
	}
	if strings.ContainsAny(r.TxID, "/\\:\x00") || strings.Contains(r.TxID, "..") || r.ActivePath != r.Target ||
		r.StagingPath != fmt.Sprintf(".staging.%s.%s", r.Target, r.TxID) {
		return false
	}
	if filepath.IsAbs(r.ActivePath) || filepath.IsAbs(r.StagingPath) || filepath.IsAbs(r.BackupPath) || filepath.IsAbs(r.QuarantinePath) {
		return false
	}
	if strings.ContainsAny(r.TargetVer, "/\\:\x00") || strings.Contains(r.TargetVer, "..") {
		return false
	}
	if r.BackupPath != "" && r.BackupPath != fmt.Sprintf(".backup.%s.%s", r.Target, r.TxID) {
		return false
	}
	if r.QuarantinePath != "" && r.QuarantinePath != fmt.Sprintf(".quarantine.%s.%s", r.Target, r.TxID) {
		return false
	}
	switch r.State {
	case JournalStateBackingUp, JournalStateBackedUp:
		if r.BackupPath == "" || !r.HadActive || !isSHA256(r.BackupHash) {
			return false
		}
	case JournalStatePreparing, JournalStateStaged:
		if r.BackupPath != "" || (r.HadActive && !isSHA256(r.BackupHash)) || (r.State == JournalStateStaged && !isSHA256(r.StagingHash)) {
			return false
		}
	case JournalStateIdle:
		if r.BackupPath != "" || (r.HadActive && !isSHA256(r.ActiveHash)) {
			return false
		}
	case JournalStateActivating, JournalStateActivated:
		if r.HadActive && (!isSHA256(r.BackupHash) || r.BackupPath == "") {
			return false
		}
		if !isSHA256(r.StagingHash) {
			return false
		}
	case JournalStateRollingBack, JournalStateRolledBack:
		if !r.HadActive || !isSHA256(r.BackupHash) || r.BackupPath == "" {
			return false
		}
		if r.State == JournalStateRolledBack && r.ActiveHash != r.BackupHash {
			return false
		}
	case JournalStateCommitted:
		if !isSHA256(r.ActiveHash) || !isSHA256(r.StagingHash) || (r.HadActive && !isSHA256(r.BackupHash)) {
			return false
		}
	case JournalStateFailed:
	default:
		return false
	}
	for _, digest := range []string{r.BackupHash, r.StagingHash, r.ActiveHash} {
		if digest != "" && !isSHA256(digest) {
			return false
		}
	}
	return r.RecordHash == r.computeHash()
}

func validTargetName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name &&
		filepath.Clean(name) == name && !strings.ContainsAny(name, "/\\:\x00")
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

// syncAndHashTree fsyncs every regular payload file and directory before returning
// a deterministic digest. Symlinks and special files are rejected. The digest is
// transaction evidence for accidental corruption, not an authenticity signature.
func (m *RecoveryManager) syncAndHashTree(root string) (string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("runtime tree root is not a real directory: %s", root)
	}

	var paths []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("runtime tree contains a symlink or special file: %s", path)
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return "", err
	}

	h := sha256.New()
	var directories []string
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		if rel == "." {
			rel = ""
		}
		kind := "file"
		if info.IsDir() {
			kind = "dir"
			directories = append(directories, path)
		}
		if _, err := fmt.Fprintf(h, "%s:%d:%s:%d:%d:", kind, len(rel), rel, info.Mode().Perm(), info.Size()); err != nil {
			return "", err
		}
		if info.IsDir() {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		openedInfo, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
			_ = f.Close()
			if statErr != nil {
				return "", statErr
			}
			return "", fmt.Errorf("runtime file changed during verification: %s", path)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("sync runtime file %s: %w", path, err)
		}
		if _, err := io.Copy(h, f); err != nil {
			_ = f.Close()
			return "", err
		}
		finalInfo, statErr := f.Stat()
		pathInfo, pathErr := os.Lstat(path)
		if statErr != nil || pathErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) || finalInfo.Size() != openedInfo.Size() || !finalInfo.ModTime().Equal(openedInfo.ModTime()) {
			_ = f.Close()
			if statErr != nil {
				return "", statErr
			}
			if pathErr != nil {
				return "", pathErr
			}
			return "", fmt.Errorf("runtime file changed during integrity validation: %s", path)
		}
		if err := f.Close(); err != nil {
			return "", err
		}
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := m.syncDirectory(directories[i]); err != nil {
			return "", fmt.Errorf("sync runtime directory %s: %w", directories[i], err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FaultInjector allows synthetic injection of failures to test crash and recovery behavior.
type FaultInjector struct {
	BeforeStateChange func(JournalState) error
	AfterStateChange  func(JournalState) error
	BeforeRename      func(from, to string) error
	AfterRename       func(from, to string) error
	BeforeSync        func(path string) error
}

// Global in-memory mutexes per canonical base path for dual-layer locking.
var (
	recoveryLocksMu sync.Mutex
	recoveryLocks   = make(map[string]*sync.Mutex)
)

// RecoveryManager manages crash-safe atomic activation, write-ahead journaling,
// and idempotent reconciliation for runtime directories.
type RecoveryManager struct {
	baseDir       string
	journalPath   string
	lockPath      string
	pathMutex     *sync.Mutex
	lockFile      *os.File
	currentTx     *JournalRecord
	faultInjector *FaultInjector
	syncDirectory func(string) error
}

// NewRecoveryManager initializes a RecoveryManager confined to baseDir.
func NewRecoveryManager(baseDir string) (*RecoveryManager, error) {
	cleanBase, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base directory: %w", err)
	}
	if err := os.MkdirAll(cleanBase, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	canonBase, err := filepath.EvalSymlinks(cleanBase)
	if err != nil {
		canonBase = cleanBase
	}

	recoveryLocksMu.Lock()
	pMu, exists := recoveryLocks[canonBase]
	if !exists {
		pMu = &sync.Mutex{}
		recoveryLocks[canonBase] = pMu
	}
	recoveryLocksMu.Unlock()

	return &RecoveryManager{
		baseDir:       canonBase,
		journalPath:   filepath.Join(canonBase, "recovery.journal"),
		lockPath:      filepath.Join(canonBase, ".recovery.lock"),
		pathMutex:     pMu,
		syncDirectory: syncDir,
	}, nil
}

// SetFaultInjector sets fault injection hooks for adversarial test execution.
func (m *RecoveryManager) SetFaultInjector(fi *FaultInjector) {
	m.faultInjector = fi
}

// AcquireLock acquires in-process mutex and cross-process advisory lock.
func (m *RecoveryManager) AcquireLock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !m.pathMutex.TryLock() {
		return &SafeError{
			Code:        ErrCodeRecoveryLockHeld,
			SafeMessage: fmt.Sprintf("In-process recovery lock already held for: %s", m.baseDir),
			Recoverable: false,
			Err:         ErrRecoveryLockHeld,
		}
	}
	if info, err := os.Lstat(m.lockPath); err == nil && !info.Mode().IsRegular() {
		m.pathMutex.Unlock()
		return fmt.Errorf("%w: recovery lock must be a regular file", ErrSecurityEscapingSymlink)
	} else if err != nil && !os.IsNotExist(err) {
		m.pathMutex.Unlock()
		return fmt.Errorf("failed to inspect recovery lock file: %w", err)
	}

	f, err := os.OpenFile(m.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		m.pathMutex.Unlock()
		return fmt.Errorf("failed to open recovery lock file %s: %w", m.lockPath, err)
	}

	if err := fileLock(f); err != nil {
		_ = f.Close()
		m.pathMutex.Unlock()
		return &SafeError{
			Code:        ErrCodeRecoveryLockHeld,
			SafeMessage: fmt.Sprintf("Cross-process recovery lock held by another process: %s", m.lockPath),
			Recoverable: false,
			Err:         ErrRecoveryLockHeld,
		}
	}

	m.lockFile = f
	return nil
}

// ReleaseLock releases both lock layers safely.
func (m *RecoveryManager) ReleaseLock() {
	if m == nil || m.lockFile == nil {
		return
	}
	_ = fileUnlock(m.lockFile)
	_ = m.lockFile.Close()
	m.lockFile = nil
	m.pathMutex.Unlock()
}

func (m *RecoveryManager) requireLock() error {
	if m == nil || m.lockFile == nil {
		return &SafeError{Code: ErrCodeRecoveryLockRequired, SafeMessage: "Acquire the recovery lock before filesystem operations", Recoverable: true, Err: ErrRecoveryLockNotHeld}
	}
	return nil
}

// validateConfinement guarantees that a path is strictly inside baseDir and does not escape via symlinks.
func (m *RecoveryManager) validateConfinement(targetPath string) (string, error) {
	fullPath := targetPath
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(m.baseDir, targetPath)
	}
	cleanFull := filepath.Clean(fullPath)

	rel, err := filepath.Rel(m.baseDir, cleanFull)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("%w: path %s escapes base %s", ErrSecurityEscapingSymlink, cleanFull, m.baseDir)
	}

	// If path exists, check resolved symlinks
	if eval, err := filepath.EvalSymlinks(cleanFull); err == nil {
		relEval, err := filepath.Rel(m.baseDir, eval)
		if err != nil || strings.HasPrefix(relEval, "..") || relEval == ".." {
			return "", fmt.Errorf("%w: symlink %s resolves to %s outside base %s", ErrSecurityEscapingSymlink, cleanFull, eval, m.baseDir)
		}
		return eval, nil
	}

	// If path does not exist yet, check its closest existing parent directory
	parent := filepath.Dir(cleanFull)
	for parent != m.baseDir && parent != "/" && parent != "." {
		if evalParent, err := filepath.EvalSymlinks(parent); err == nil {
			relParent, err := filepath.Rel(m.baseDir, evalParent)
			if err != nil || strings.HasPrefix(relParent, "..") || relParent == ".." {
				return "", fmt.Errorf("%w: parent symlink %s resolves to %s outside base %s", ErrSecurityEscapingSymlink, parent, evalParent, m.baseDir)
			}
			break
		}
		parent = filepath.Dir(parent)
	}

	return cleanFull, nil
}

// syncDir issues fsync on a directory to guarantee directory entry updates are durable.
func syncDir(dirPath string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (m *RecoveryManager) writeJournal(record *JournalRecord) error {
	record.RecordHash = record.computeHash()

	if m.faultInjector != nil && m.faultInjector.BeforeStateChange != nil {
		if err := m.faultInjector.BeforeStateChange(record.State); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal journal record: %w", err)
	}

	f, err := os.CreateTemp(m.baseDir, ".recovery.journal.tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary journal: %w", err)
	}
	tmpJournal := f.Name()
	if err := f.Chmod(0o600); err != nil {
		return errors.Join(fmt.Errorf("failed to set temporary journal permissions: %w", err), f.Close(), os.Remove(tmpJournal))
	}

	n, err := f.Write(data)
	if err != nil || n != len(data) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return errors.Join(fmt.Errorf("failed to write temporary journal: %w", err), f.Close(), os.Remove(tmpJournal))
	}

	if m.faultInjector != nil && m.faultInjector.BeforeSync != nil {
		if err := m.faultInjector.BeforeSync(tmpJournal); err != nil {
			return errors.Join(err, f.Close(), os.Remove(tmpJournal))
		}
	}

	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("failed to fsync temporary journal: %w", err), f.Close(), os.Remove(tmpJournal))
	}
	if err := f.Close(); err != nil {
		return errors.Join(fmt.Errorf("failed to close temporary journal: %w", err), os.Remove(tmpJournal))
	}

	if err := os.Rename(tmpJournal, m.journalPath); err != nil {
		return errors.Join(fmt.Errorf("failed to atomic rename journal to %s: %w", m.journalPath, err), os.Remove(tmpJournal))
	}

	if err := m.syncDirectory(m.baseDir); err != nil {
		return fmt.Errorf("failed to fsync base directory after journal update: %w", err)
	}

	if m.faultInjector != nil && m.faultInjector.AfterStateChange != nil {
		if err := m.faultInjector.AfterStateChange(record.State); err != nil {
			return err
		}
	}

	m.currentTx = record
	return nil
}

func (m *RecoveryManager) readJournal() (*JournalRecord, error) {
	info, err := os.Lstat(m.journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &SafeError{Code: ErrCodeJournalCorrupt, SafeMessage: "Journal must be a regular file", Recoverable: false, Err: ErrJournalCorrupt}
	}
	data, err := os.ReadFile(m.journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	if len(data) == 0 {
		return nil, &SafeError{
			Code:        ErrCodeJournalCorrupt,
			SafeMessage: "Journal file is empty or truncated",
			Recoverable: false,
			Err:         ErrJournalCorrupt,
		}
	}

	var rec JournalRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, &SafeError{
			Code:        ErrCodeJournalCorrupt,
			SafeMessage: fmt.Sprintf("Journal JSON malformed: %v", err),
			Recoverable: false,
			Err:         ErrJournalCorrupt,
		}
	}

	if !rec.isValid() {
		return nil, &SafeError{
			Code:        ErrCodeJournalCorrupt,
			SafeMessage: fmt.Sprintf("Journal checksum mismatch: stored %s != computed %s", rec.RecordHash, rec.computeHash()),
			Recoverable: false,
			Err:         ErrJournalCorrupt,
		}
	}
	for _, path := range []string{rec.ActivePath, rec.StagingPath, rec.BackupPath} {
		if path == "" {
			continue
		}
		if _, err := m.validateConfinement(path); err != nil {
			return nil, &SafeError{Code: ErrCodeJournalCorrupt, SafeMessage: "Journal contains an unsafe runtime path", Recoverable: false, Err: ErrJournalCorrupt}
		}
	}

	return &rec, nil
}

// Begin initiates a new recovery transaction and creates an isolated staging directory.
func (m *RecoveryManager) Begin(ctx context.Context, targetName, targetVer, expectedHash string) (string, error) {
	if err := m.requireLock(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if m.currentTx == nil {
		existing, err := m.readJournal()
		if err != nil {
			return "", err
		}
		if existing != nil {
			m.currentTx = existing
		}
	}
	if m.currentTx != nil && m.currentTx.State != JournalStateIdle && m.currentTx.State != JournalStateCommitted && m.currentTx.State != JournalStateRolledBack {
		return "", fmt.Errorf("%w: reconcile the persisted transaction before beginning another", ErrTransactionInProgress)
	}

	if !validTargetName(targetName) {
		return "", fmt.Errorf("%w: target must be a single safe directory name", ErrSecurityEscapingSymlink)
	}
	if _, err := m.validateConfinement(targetName); err != nil {
		return "", err
	}
	activeFull, err := m.validateConfinement(targetName)
	if err != nil {
		return "", err
	}
	activeExists, err := realDirectoryExists(activeFull)
	if err != nil {
		return "", fmt.Errorf("active runtime cannot be safely inspected: %w", err)
	}
	var activeHash string
	if activeExists {
		activeHash, err = m.syncAndHashTree(activeFull)
		if err != nil {
			return "", fmt.Errorf("active runtime cannot be durably verified before staging: %w", err)
		}
		if m.currentTx != nil && m.currentTx.ActiveHash != "" {
			if err := m.verifyTreeHash(activeFull, m.currentTx.ActiveHash); err != nil {
				return "", fmt.Errorf("existing active runtime does not match its committed evidence: %w", err)
			}
		}
		if m.currentTx != nil && m.currentTx.ActiveHash == "" && m.currentTx.State == JournalStateIdle {
			return "", errors.New("active runtime exists without committed integrity evidence")
		}
	}

	txID := fmt.Sprintf("tx_%d_%d", os.Getpid(), time.Now().UnixNano())
	stagingRel := fmt.Sprintf(".staging.%s.%s", targetName, txID)
	stagingFull, err := m.validateConfinement(stagingRel)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(stagingFull, 0o755); err != nil {
		return "", fmt.Errorf("failed to create staging directory: %w", err)
	}

	rec := &JournalRecord{
		Version:      1,
		TxID:         txID,
		State:        JournalStatePreparing,
		Target:       targetName,
		TargetVer:    targetVer,
		ActivePath:   targetName,
		StagingPath:  stagingRel,
		BackupPath:   "",
		ExpectedHash: expectedHash,
		HadActive:    activeExists,
		BackupHash:   activeHash,
		ActiveHash:   activeHash,
		Timestamp:    time.Now().UTC(),
	}

	if err := m.writeJournal(rec); err != nil {
		// The journal rename may have happened even when its directory sync failed.
		// Preserve staging so either possible on-disk journal state remains recoverable.
		return "", err
	}

	return stagingFull, nil
}

// CommitStaged verifies staging, atomically backs up active, swaps staging into active,
// verifies active, and marks transaction COMMITTED. If anything fails, it rolls back safely.
func (m *RecoveryManager) CommitStaged(ctx context.Context, verifyFn func(path string) error) error {
	if err := m.requireLock(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if verifyFn == nil {
		return errors.New("staged runtime verification function is required")
	}
	if m.currentTx == nil || m.currentTx.State != JournalStatePreparing {
		return ErrTransactionNotInProgress
	}

	stagingFull := filepath.Join(m.baseDir, m.currentTx.StagingPath)
	activeFull := filepath.Join(m.baseDir, m.currentTx.ActivePath)

	// 1. Verify staging directory contents
	if err := verifyFn(stagingFull); err != nil {
		if removeErr := removeConfinedTree(m, stagingFull); removeErr != nil {
			return fmt.Errorf("staging verification failed (%v); staging cleanup failed: %w", err, removeErr)
		}
		m.currentTx.State = JournalStateIdle
		if journalErr := m.writeJournal(m.currentTx); journalErr != nil {
			return fmt.Errorf("staging verification failed (%v); unable to persist idle state: %w", err, journalErr)
		}
		return fmt.Errorf("staging verification failed: %w", err)
	}
	stagingHash, err := m.syncAndHashTree(stagingFull)
	if err != nil {
		return fmt.Errorf("staging durability/integrity validation failed: %w", err)
	}

	// 2. Mark STAGED
	m.currentTx.State = JournalStateStaged
	m.currentTx.StagingHash = stagingHash
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return err
	}

	var backupFull string
	activeFi, activeErr := os.Lstat(activeFull)
	activeExists := activeErr == nil
	if activeErr != nil && !os.IsNotExist(activeErr) {
		return fmt.Errorf("failed to inspect active runtime: %w", activeErr)
	}
	if activeExists && (!activeFi.IsDir() || activeFi.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("active runtime is not a real directory; refusing activation")
	}
	if activeExists != m.currentTx.HadActive {
		return fmt.Errorf("active runtime presence changed during transaction; refusing activation")
	}

	// 3. Backup active if it exists
	if activeExists {
		backupRel := fmt.Sprintf(".backup.%s.%s", m.currentTx.Target, m.currentTx.TxID)
		var err error
		backupFull, err = m.validateConfinement(backupRel)
		if err != nil {
			return err
		}

		backupHash, hashErr := m.syncAndHashTree(activeFull)
		if hashErr != nil {
			return fmt.Errorf("active runtime cannot be durably verified before backup: %w", hashErr)
		}
		if backupHash != m.currentTx.BackupHash {
			return fmt.Errorf("active runtime changed after transaction began; refusing activation")
		}
		m.currentTx.BackupPath = backupRel
		m.currentTx.State = JournalStateBackingUp
		m.currentTx.Timestamp = time.Now().UTC()
		if err := m.writeJournal(m.currentTx); err != nil {
			return err
		}

		if m.faultInjector != nil && m.faultInjector.BeforeRename != nil {
			if err := m.faultInjector.BeforeRename(activeFull, backupFull); err != nil {
				return err
			}
		}

		if err := os.Rename(activeFull, backupFull); err != nil {
			return fmt.Errorf("failed to move active to backup: %w", err)
		}
		if err := m.syncDirectory(m.baseDir); err != nil {
			return fmt.Errorf("failed to sync directory after backup rename: %w", err)
		}

		if m.faultInjector != nil && m.faultInjector.AfterRename != nil {
			if err := m.faultInjector.AfterRename(activeFull, backupFull); err != nil {
				return err
			}
		}

		m.currentTx.State = JournalStateBackedUp
		m.currentTx.Timestamp = time.Now().UTC()
		if err := m.writeJournal(m.currentTx); err != nil {
			return err
		}
	}
	if !activeExists {
		m.currentTx.HadActive = false
		m.currentTx.BackupHash = ""
		m.currentTx.BackupPath = ""
	}

	// 4. Activate staging -> active
	m.currentTx.State = JournalStateActivating
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return err
	}

	if m.faultInjector != nil && m.faultInjector.BeforeRename != nil {
		if err := m.faultInjector.BeforeRename(stagingFull, activeFull); err != nil {
			return err
		}
	}

	if err := os.Rename(stagingFull, activeFull); err != nil {
		// Staging rename failed! If backup exists, restore active from backup immediately
		if backupFull != "" {
			return m.rollbackInternal(activeFull, backupFull, err)
		}
		return fmt.Errorf("failed to activate staging to active: %w", err)
	}
	if err := m.syncDirectory(m.baseDir); err != nil {
		return fmt.Errorf("failed to sync directory after activation: %w", err)
	}

	if m.faultInjector != nil && m.faultInjector.AfterRename != nil {
		if err := m.faultInjector.AfterRename(stagingFull, activeFull); err != nil {
			return err
		}
	}

	m.currentTx.State = JournalStateActivated
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return err
	}

	// 5. Post-activation verification
	if err := ctx.Err(); err != nil {
		return m.rollbackInternal(activeFull, backupFull, err)
	}
	if err := verifyFn(activeFull); err != nil {
		return m.rollbackInternal(activeFull, backupFull, err)
	}
	activeHash, err := m.syncAndHashTree(activeFull)
	if err != nil || activeHash != m.currentTx.StagingHash {
		if err == nil {
			err = errors.New("active runtime tree differs from verified staging tree")
		}
		return m.rollbackInternal(activeFull, backupFull, err)
	}

	// 6. Commit transaction
	m.currentTx.ActiveHash = activeHash
	m.currentTx.State = JournalStateCommitted
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return err
	}

	// Remove backup only after verification and commit succeed
	if backupFull != "" {
		if _, err := m.validateConfinement(backupFull); err != nil {
			return fmt.Errorf("committed runtime is verified, but backup confinement check failed; backup preserved: %w", err)
		}
		if err := m.verifyTreeHash(backupFull, m.currentTx.BackupHash); err != nil {
			return fmt.Errorf("committed runtime is verified, but backup failed its transaction integrity check; backup preserved: %w", err)
		}
		if err := os.RemoveAll(backupFull); err != nil {
			return fmt.Errorf("runtime committed; unable to prune verified backup: %w", err)
		}
		if err := m.syncDirectory(m.baseDir); err != nil {
			return fmt.Errorf("runtime committed; backup cleanup sync failed: %w", err)
		}
	}

	m.currentTx.State = JournalStateIdle
	m.currentTx.BackupPath = ""
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return fmt.Errorf("runtime committed but idle journal update failed: %w", err)
	}

	return nil
}

func (m *RecoveryManager) rollbackInternal(activeFull, backupFull string, causeErr error) error {
	if m.currentTx == nil {
		return fmt.Errorf("cannot rollback without a transaction record: %w", causeErr)
	}
	if !m.currentTx.HadActive || backupFull == "" || m.currentTx.BackupHash == "" {
		return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "No verified prior runtime is available for rollback; current files and journal are preserved", Recoverable: false, Err: errors.Join(ErrRecoveryAmbiguous, causeErr)}
	}
	backupFull, err := m.validateConfinement(backupFull)
	if err != nil {
		return fmt.Errorf("backup path failed confinement; preserving runtime: %w", err)
	}
	backupHash, err := m.hashRuntimeTree(backupFull)
	if err != nil || backupHash != m.currentTx.BackupHash {
		if err == nil {
			err = errors.New("backup runtime digest does not match the transaction journal")
		}
		return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Required backup is missing or failed integrity validation; active and backup artifacts are preserved", Recoverable: false, Err: errors.Join(ErrRecoveryAmbiguous, err)}
	}
	if _, err := m.validateConfinement(activeFull); err != nil {
		return fmt.Errorf("active path failed confinement; preserving backup: %w", err)
	}
	quarantineRel := fmt.Sprintf(".quarantine.%s.%s", m.currentTx.Target, m.currentTx.TxID)
	quarantineFull, err := m.validateConfinement(quarantineRel)
	if err != nil {
		return err
	}
	m.currentTx.QuarantinePath = quarantineRel
	m.currentTx.State = JournalStateRollingBack
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return fmt.Errorf("failed to persist rollback intent; active and backup preserved: %w", err)
	}

	if _, err := os.Lstat(quarantineFull); err == nil {
		return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Rollback quarantine already exists alongside the active runtime; preserving active, backup, and quarantine", Recoverable: false, Err: ErrRecoveryAmbiguous}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Lstat(activeFull); err == nil {
		if m.faultInjector != nil && m.faultInjector.BeforeRename != nil {
			if err := m.faultInjector.BeforeRename(activeFull, quarantineFull); err != nil {
				return rollbackRequiredError("failed to quarantine unverified active runtime; backup preserved", err)
			}
		}
		if err := os.Rename(activeFull, quarantineFull); err != nil {
			return rollbackRequiredError("failed to quarantine unverified active runtime; backup preserved", err)
		}
		if err := m.syncDirectory(m.baseDir); err != nil {
			return rollbackRequiredError("active quarantined but parent directory sync failed; backup preserved", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if m.faultInjector != nil && m.faultInjector.BeforeRename != nil {
		if err := m.faultInjector.BeforeRename(backupFull, activeFull); err != nil {
			return rollbackRequiredError("verified backup preserved after injected restore failure", err)
		}
	}
	if err := os.Rename(backupFull, activeFull); err != nil {
		return rollbackRequiredError("verified backup preserved after restore rename failure", err)
	}
	if err := m.syncDirectory(m.baseDir); err != nil {
		return rollbackRequiredError("backup moved to active but parent directory sync failed; journal retained", err)
	}
	if err := m.verifyTreeHash(activeFull, m.currentTx.BackupHash); err != nil {
		return rollbackRequiredError("restored runtime verification failed; journal and quarantine retained", err)
	}
	m.currentTx.State = JournalStateRolledBack
	m.currentTx.ActiveHash = m.currentTx.BackupHash
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return fmt.Errorf("runtime rollback completed but journal update failed: %w", err)
	}

	return fmt.Errorf("post-activation verification failed, rolled back to prior version: %w", causeErr)
}

func rollbackRequiredError(message string, err error) error {
	return &SafeError{Code: ErrCodeRollbackFailed, SafeMessage: message, Recoverable: false, Err: errors.Join(ErrRollbackFailed, err)}
}

func (m *RecoveryManager) hashRuntimeTree(path string) (string, error) {
	if _, err := m.validateConfinement(path); err != nil {
		return "", err
	}
	return m.syncAndHashTree(path)
}

func (m *RecoveryManager) verifyTreeHash(path, expected string) error {
	if expected == "" || !isSHA256(expected) {
		return errors.New("runtime integrity evidence is absent or invalid")
	}
	actual, err := m.hashRuntimeTree(path)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("runtime tree SHA-256 mismatch: got %s", actual)
	}
	return nil
}

// Reconcile performs persistent crash recovery by checking disk evidence and the journal.
// It is strictly idempotent and protects backups from being deleted in ambiguous states.
func (m *RecoveryManager) Reconcile(ctx context.Context) error {
	if err := m.requireLock(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rec, err := m.readJournal()
	if err != nil {
		return err // ErrJournalCorrupt or read error: fail-closed
	}

	if rec == nil {
		// No journal file: inspect disk for un-journaled or legacy artifacts
		return m.reconcileUnjournaled()
	}
	m.currentTx = rec

	activeFull := filepath.Join(m.baseDir, rec.ActivePath)
	stagingFull := filepath.Join(m.baseDir, rec.StagingPath)
	backupFull := ""
	if rec.BackupPath != "" {
		backupFull = filepath.Join(m.baseDir, rec.BackupPath)
	}

	activeExists, err := realDirectoryExists(activeFull)
	if err != nil {
		return fmt.Errorf("failed to inspect active runtime: %w", err)
	}
	backupExists := false
	if backupFull != "" {
		backupExists, err = realDirectoryExists(backupFull)
		if err != nil {
			return fmt.Errorf("failed to inspect runtime backup: %w", err)
		}
	}

	switch rec.State {
	case JournalStateIdle:
		if rec.ActiveHash != "" {
			if err := m.verifyTreeHash(activeFull, rec.ActiveHash); err != nil {
				return &SafeError{Code: ErrCodeRecoveryFailed, SafeMessage: "Transaction is finalized but runtime integrity is not certified; artifacts are preserved", Recoverable: false, Err: errors.Join(ErrRecoveryAmbiguous, err)}
			}
		} else if activeExists || rec.HadActive {
			return &SafeError{Code: ErrCodeRecoveryFailed, SafeMessage: "IDLE state lacks integrity evidence for the expected active runtime; artifacts are preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		if stagingFull != "" {
			if err := removeConfinedTree(m, stagingFull); err != nil {
				return err
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		}
		return nil

	case JournalStateCommitted:
		if !activeExists {
			return &SafeError{Code: ErrCodeRecoveryFailed, SafeMessage: "Journal state is COMMITTED but active runtime is missing on disk; artifacts are preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		if err := m.verifyTreeHash(activeFull, rec.ActiveHash); err != nil {
			return &SafeError{Code: ErrCodeRecoveryFailed, SafeMessage: "Transaction is finalized as COMMITTED but active runtime integrity does not match; artifacts are preserved", Recoverable: false, Err: errors.Join(ErrRecoveryAmbiguous, err)}
		}
		if backupFull != "" && backupExists {
			if err := removeConfinedTree(m, backupFull); err != nil {
				return err
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		}
		if stagingFull != "" {
			if err := removeConfinedTree(m, stagingFull); err != nil {
				return err
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		}
		rec.State = JournalStateIdle
		rec.Timestamp = time.Now().UTC()
		return m.writeJournal(rec)

	case JournalStatePreparing, JournalStateStaged:
		// The active tree has not entered a swap; verify prior evidence before cleanup.
		if rec.HadActive {
			if err := m.verifyTreeHash(activeFull, rec.BackupHash); err != nil {
				return &SafeError{Code: ErrCodeRecoveryFailed, SafeMessage: "Previous runtime cannot be verified; staging and active files are preserved", Recoverable: false, Err: errors.Join(ErrRecoveryAmbiguous, err)}
			}
			rec.ActiveHash = rec.BackupHash
		} else if activeExists {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "An active runtime appeared during a first-install staging transaction; files are preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		if stagingFull != "" {
			if err := removeConfinedTree(m, stagingFull); err != nil {
				return err
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		}
		rec.State = JournalStateIdle
		rec.Timestamp = time.Now().UTC()
		return m.writeJournal(rec)

	case JournalStateBackingUp:
		if activeExists && backupExists {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Active and backup both exist during the backup transition; neither was modified", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		return m.reconcileRollback(rec, activeFull, backupFull, activeExists, backupExists, stagingFull)

	case JournalStateBackedUp:
		return m.reconcileRollback(rec, activeFull, backupFull, activeExists, backupExists, stagingFull)

	case JournalStateActivating, JournalStateActivated:
		return m.reconcileRollback(rec, activeFull, backupFull, activeExists, backupExists, stagingFull)

	case JournalStateRollingBack:
		return m.reconcileRollback(rec, activeFull, backupFull, activeExists, backupExists, stagingFull)

	case JournalStateRolledBack:
		if !rec.HadActive || rec.BackupHash == "" {
			return &SafeError{Code: ErrCodeRecoveryFailed, SafeMessage: "Rollback has no evidence of a prior runtime; journal and artifacts are preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		if err := m.verifyTreeHash(activeFull, rec.BackupHash); err != nil {
			return &SafeError{Code: ErrCodeRecoveryFailed, SafeMessage: "Rollback state is not backed by a valid restored runtime; artifacts are preserved", Recoverable: false, Err: errors.Join(ErrRecoveryAmbiguous, err)}
		}
		if stagingFull != "" {
			if err := removeConfinedTree(m, stagingFull); err != nil {
				return err
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		}
		rec.ActiveHash = rec.BackupHash
		rec.BackupPath = ""
		rec.State = JournalStateIdle
		rec.Timestamp = time.Now().UTC()
		return m.writeJournal(rec)

	case JournalStateFailed:
		return &SafeError{
			Code:        ErrCodeRecoveryFailed,
			SafeMessage: "Transaction was marked FAILED; manual recovery required. Backups are preserved.",
			Recoverable: false,
			Err:         ErrRollbackFailed,
		}
	}

	return &SafeError{Code: ErrCodeRecoveryFailed, SafeMessage: "Unsupported journal state; all recovery artifacts are preserved", Recoverable: false, Err: ErrJournalCorrupt}
}

func realDirectoryExists(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("path is not a real directory: %s", path)
	}
	return true, nil
}

func removeConfinedTree(m *RecoveryManager, path string) error {
	if _, err := m.validateConfinement(path); err != nil {
		return err
	}
	exists, err := realDirectoryExists(path)
	if err != nil || !exists {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return m.syncDirectory(m.baseDir)
}

func (m *RecoveryManager) reconcileRollback(rec *JournalRecord, active, backup string, activeExists, backupExists bool, staging string) error {
	var err error
	if !rec.HadActive || !isSHA256(rec.BackupHash) || backup == "" {
		return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "No transaction-bound, verified prior runtime is available; active, staging, and journal are preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
	}
	if backupExists {
		if err := m.verifyTreeHash(backup, rec.BackupHash); err != nil {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Required backup is corrupt or unverifiable; active, backup, staging, and journal are preserved", Recoverable: false, Err: errors.Join(ErrRecoveryAmbiguous, err)}
		}
	} else {
		// The backup may already have been renamed to active by an interrupted
		// recovery. Accept only an exact match to the transaction-bound digest.
		if !activeExists || m.verifyTreeHash(active, rec.BackupHash) != nil {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Required backup is missing and active runtime does not prove restoration; all artifacts are preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		rec.ActiveHash = rec.BackupHash
		rec.State = JournalStateRolledBack
		rec.Timestamp = time.Now().UTC()
		if err := m.writeJournal(rec); err != nil {
			return err
		}
		return removeConfinedTree(m, staging)
	}

	quarantine := ""
	if activeExists {
		if rec.QuarantinePath == "" {
			rec.QuarantinePath = fmt.Sprintf(".quarantine.%s.%s", rec.Target, rec.TxID)
		}
		quarantine, err = m.validateConfinement(rec.QuarantinePath)
		if err != nil {
			return err
		}
		if err := m.writeJournal(rec); err != nil {
			return err
		}
		quarantineExists, err := realDirectoryExists(quarantine)
		if err != nil || quarantineExists {
			if err != nil {
				return err
			}
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Active runtime and rollback quarantine both exist; preserving active, backup, and quarantine", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		if m.faultInjector != nil && m.faultInjector.BeforeRename != nil {
			if err := m.faultInjector.BeforeRename(active, quarantine); err != nil {
				return rollbackRequiredError("failed to quarantine active runtime; verified backup preserved", err)
			}
		}
		if err := os.Rename(active, quarantine); err != nil {
			return rollbackRequiredError("failed to quarantine active runtime; verified backup preserved", err)
		}
		if err := m.syncDirectory(m.baseDir); err != nil {
			return rollbackRequiredError("active quarantined but directory sync failed; verified backup preserved", err)
		}
	}
	if m.faultInjector != nil && m.faultInjector.BeforeRename != nil {
		if err := m.faultInjector.BeforeRename(backup, active); err != nil {
			return rollbackRequiredError("verified backup preserved after restore interruption", err)
		}
	}
	if err := os.Rename(backup, active); err != nil {
		return rollbackRequiredError("failed to restore verified backup; backup remains recoverable", err)
	}
	if err := m.syncDirectory(m.baseDir); err != nil {
		return rollbackRequiredError("backup moved to active but parent directory sync failed", err)
	}
	if err := m.verifyTreeHash(active, rec.BackupHash); err != nil {
		return rollbackRequiredError("restored runtime did not verify; journal and quarantine preserved", err)
	}
	rec.ActiveHash = rec.BackupHash
	rec.State = JournalStateRolledBack
	rec.Timestamp = time.Now().UTC()
	if err := m.writeJournal(rec); err != nil {
		return err
	}
	return removeConfinedTree(m, staging)
}

func (m *RecoveryManager) reconcileUnjournaled() error {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return err
	}
	var backups, stagings []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".backup.") {
			backups = append(backups, filepath.Join(m.baseDir, name))
		} else if strings.HasPrefix(name, ".staging.") {
			if !e.IsDir() {
				return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Unjournaled staging artifact is not a directory; artifact preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
			}
			stagings = append(stagings, filepath.Join(m.baseDir, name))
		}
	}
	if len(backups) > 0 {
		return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Unjournaled backups lack transaction-bound integrity evidence; every backup and active path is preserved for operator review", Recoverable: false, Err: ErrRecoveryAmbiguous}
	}
	for _, s := range stagings {
		if err := removeConfinedTree(m, s); err != nil {
			return err
		}
	}
	return nil
}
