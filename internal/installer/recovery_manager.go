package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	ActivePath   string       `json:"active_path"`  // Relative to baseDir
	StagingPath  string       `json:"staging_path"` // Relative to baseDir
	BackupPath   string       `json:"backup_path"`  // Relative to baseDir
	ExpectedHash string       `json:"expected_hash"`
	Timestamp    time.Time    `json:"timestamp"`
	RecordHash   string       `json:"record_hash"` // SHA-256 of payload fields
}

func (r *JournalRecord) computeHash() string {
	payload := fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%s|%s|%d",
		r.Version,
		r.TxID,
		r.State,
		r.Target,
		r.TargetVer,
		r.ActivePath,
		r.StagingPath,
		r.BackupPath,
		r.ExpectedHash,
		r.Timestamp.UnixNano(),
	)
	h := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(h[:])
}

func (r *JournalRecord) isValid() bool {
	if r == nil || r.Version != 1 || r.TxID == "" || !validTargetName(r.Target) || r.Timestamp.IsZero() {
		return false
	}
	if strings.ContainsAny(r.TxID, "/\\:\x00") || strings.Contains(r.TxID, "..") || r.ActivePath != r.Target ||
		r.StagingPath != fmt.Sprintf(".staging.%s.%s", r.Target, r.TxID) {
		return false
	}
	if r.BackupPath != "" && r.BackupPath != fmt.Sprintf(".backup.%s.%s", r.Target, r.TxID) {
		return false
	}
	switch r.State {
	case JournalStateIdle, JournalStatePreparing, JournalStateStaged, JournalStateBackingUp,
		JournalStateBackedUp, JournalStateActivating, JournalStateActivated,
		JournalStateRollingBack, JournalStateRolledBack, JournalStateCommitted, JournalStateFailed:
	default:
		return false
	}
	return r.RecordHash == r.computeHash()
}

func validTargetName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name &&
		filepath.Clean(name) == name && !strings.ContainsAny(name, "/\\:\x00")
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
		_ = f.Close()
		_ = os.Remove(tmpJournal)
		return fmt.Errorf("failed to set temporary journal permissions: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpJournal)
		return fmt.Errorf("failed to write temporary journal: %w", err)
	}

	if m.faultInjector != nil && m.faultInjector.BeforeSync != nil {
		if err := m.faultInjector.BeforeSync(tmpJournal); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpJournal)
			return err
		}
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpJournal)
		return fmt.Errorf("failed to fsync temporary journal: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpJournal)
		return fmt.Errorf("failed to close temporary journal: %w", err)
	}

	if err := os.Rename(tmpJournal, m.journalPath); err != nil {
		_ = os.Remove(tmpJournal)
		return fmt.Errorf("failed to atomic rename journal to %s: %w", m.journalPath, err)
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
		Timestamp:    time.Now().UTC(),
	}

	if err := m.writeJournal(rec); err != nil {
		_ = os.RemoveAll(stagingFull)
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
		if removeErr := os.RemoveAll(stagingFull); removeErr != nil {
			return fmt.Errorf("staging verification failed (%v); staging cleanup failed: %w", err, removeErr)
		}
		m.currentTx.State = JournalStateIdle
		if journalErr := m.writeJournal(m.currentTx); journalErr != nil {
			return fmt.Errorf("staging verification failed (%v); unable to persist idle state: %w", err, journalErr)
		}
		return fmt.Errorf("staging verification failed: %w", err)
	}

	// 2. Mark STAGED
	m.currentTx.State = JournalStateStaged
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return err
	}

	var backupFull string
	activeFi, activeErr := os.Stat(activeFull)
	activeExists := activeErr == nil

	// 3. Backup active if it exists
	if activeExists {
		backupRel := fmt.Sprintf(".backup.%s.%s", m.currentTx.Target, m.currentTx.TxID)
		var err error
		backupFull, err = m.validateConfinement(backupRel)
		if err != nil {
			return err
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
			if restoreErr := os.Rename(backupFull, activeFull); restoreErr != nil {
				return m.failRollback(backupFull, activeFull, restoreErr)
			}
			if syncErr := m.syncDirectory(m.baseDir); syncErr != nil {
				return fmt.Errorf("backup restored but directory sync failed: %w", syncErr)
			}
		}
		m.currentTx.State = JournalStateRolledBack
		if journalErr := m.writeJournal(m.currentTx); journalErr != nil {
			return fmt.Errorf("activation failed and rollback journal update failed: %w", journalErr)
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

	// 6. Commit transaction
	m.currentTx.State = JournalStateCommitted
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return err
	}

	// Remove backup only after verification and commit succeed
	if backupFull != "" {
		if err := os.RemoveAll(backupFull); err != nil {
			return fmt.Errorf("runtime committed; unable to prune verified backup: %w", err)
		}
		if err := m.syncDirectory(m.baseDir); err != nil {
			return fmt.Errorf("runtime committed; backup cleanup sync failed: %w", err)
		}
	}

	m.currentTx.State = JournalStateIdle
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return fmt.Errorf("runtime committed but idle journal update failed: %w", err)
	}

	_ = activeFi
	return nil
}

func (m *RecoveryManager) rollbackInternal(activeFull, backupFull string, causeErr error) error {
	m.currentTx.State = JournalStateRollingBack
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return fmt.Errorf("failed to persist rollback intent; active and backup preserved: %w", err)
	}

	// Clean defective active directory
	if err := os.RemoveAll(activeFull); err != nil {
		return m.failRollback(backupFull, activeFull, err)
	}
	if err := m.syncDirectory(m.baseDir); err != nil {
		return fmt.Errorf("failed to sync active removal before rollback: %w", err)
	}

	if backupFull != "" {
		if m.faultInjector != nil && m.faultInjector.BeforeRename != nil {
			if err := m.faultInjector.BeforeRename(backupFull, activeFull); err != nil {
				m.currentTx.State = JournalStateFailed
				if journalErr := m.writeJournal(m.currentTx); journalErr != nil {
					err = errors.Join(err, journalErr)
				}
				return &SafeError{
					Code: ErrCodeRollbackFailed,
					SafeMessage: fmt.Sprintf("CRITICAL: Error restaurando backup %s a %s: %v. Backup preservado. Recuperación manual: mv %s %s",
						backupFull, activeFull, err, backupFull, activeFull),
					Recoverable: false,
					Err:         ErrRollbackFailed,
				}
			}
		}

		if err := os.Rename(backupFull, activeFull); err != nil {
			m.currentTx.State = JournalStateFailed
			if journalErr := m.writeJournal(m.currentTx); journalErr != nil {
				err = errors.Join(err, journalErr)
			}
			return &SafeError{
				Code: ErrCodeRollbackFailed,
				SafeMessage: fmt.Sprintf("CRITICAL: Error restaurando backup %s a %s: %v. Backup preservado. Recuperación manual: mv %s %s",
					backupFull, activeFull, err, backupFull, activeFull),
				Recoverable: false,
				Err:         ErrRollbackFailed,
			}
		}
		if err := m.syncDirectory(m.baseDir); err != nil {
			return fmt.Errorf("backup restored but directory sync failed: %w", err)
		}
	}

	m.currentTx.State = JournalStateRolledBack
	m.currentTx.Timestamp = time.Now().UTC()
	if err := m.writeJournal(m.currentTx); err != nil {
		return fmt.Errorf("runtime rollback completed but journal update failed: %w", err)
	}

	return fmt.Errorf("post-activation verification failed, rolled back to prior version: %w", causeErr)
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

	activeFi, activeErr := os.Stat(activeFull)
	if activeErr != nil && !os.IsNotExist(activeErr) {
		return fmt.Errorf("failed to inspect active runtime: %w", activeErr)
	}
	activeExists := activeErr == nil
	backupFi, backupErr := os.Stat(backupFull)
	if backupFull != "" && backupErr != nil && !os.IsNotExist(backupErr) {
		return fmt.Errorf("failed to inspect runtime backup: %w", backupErr)
	}
	backupExists := (backupFull != "" && backupErr == nil)

	switch rec.State {
	case JournalStateIdle, JournalStateCommitted:
		// System was in clean state. Clean any orphan staging if present.
		if stagingFull != "" {
			if err := os.RemoveAll(stagingFull); err != nil {
				return err
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		}
		return nil

	case JournalStatePreparing, JournalStateStaged:
		// Crashed before touching active. Active was untouched. Clean staging.
		if stagingFull != "" {
			if err := os.RemoveAll(stagingFull); err != nil {
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
		// Crashed during/before active -> backup
		if backupExists && activeExists {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Both active runtime and backup exist during backup transition; both preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		if backupExists && !activeExists {
			// Active was moved to backup before crash. Restore backup -> active.
			if err := os.Rename(backupFull, activeFull); err != nil {
				return m.failRollback(backupFull, activeFull, err)
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		} else if !activeExists {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Active runtime and expected backup are both missing; journal preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		if stagingFull != "" {
			if err := os.RemoveAll(stagingFull); err != nil {
				return err
			}
		}
		rec.State = JournalStateRolledBack
		rec.Timestamp = time.Now().UTC()
		return m.writeJournal(rec)

	case JournalStateBackedUp:
		// Active was moved to backup, but staging was not moved to active. Active is MISSING!
		if backupExists {
			if activeExists {
				// Both exist! Ambiguity! Check if active is empty
				if activeFi.IsDir() && dirIsEmpty(activeFull) {
					if err := os.Remove(activeFull); err != nil {
						return err
					}
					if err := os.Rename(backupFull, activeFull); err != nil {
						return m.failRollback(backupFull, activeFull, err)
					}
					if err := m.syncDirectory(m.baseDir); err != nil {
						return err
					}
				} else {
					return &SafeError{
						Code: ErrCodeRecoveryAmbiguous,
						SafeMessage: fmt.Sprintf("Ambiguous recovery: both active %s and backup %s exist with data. Backup preserved.",
							activeFull, backupFull),
						Recoverable: false,
						Err:         ErrRecoveryAmbiguous,
					}
				}
			} else {
				if err := os.Rename(backupFull, activeFull); err != nil {
					return m.failRollback(backupFull, activeFull, err)
				}
				if err := m.syncDirectory(m.baseDir); err != nil {
					return err
				}
			}
		} else {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Expected backup is missing during BACKED_UP state; journal and active files preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		if stagingFull != "" {
			if err := os.RemoveAll(stagingFull); err != nil {
				return err
			}
		}
		rec.State = JournalStateRolledBack
		rec.Timestamp = time.Now().UTC()
		return m.writeJournal(rec)

	case JournalStateActivating, JournalStateActivated:
		// Crashed during or immediately after activation.
		// If active is missing and backup exists: restore backup.
		if !activeExists && backupExists {
			if err := os.Rename(backupFull, activeFull); err != nil {
				return m.failRollback(backupFull, activeFull, err)
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
			rec.State = JournalStateRolledBack
			rec.Timestamp = time.Now().UTC()
			return m.writeJournal(rec)
		}
		// If active exists and backup exists:
		// To be fail-closed, restore backup if available and not yet verified
		if backupExists {
			if err := os.RemoveAll(activeFull); err != nil {
				return err
			}
			if err := os.Rename(backupFull, activeFull); err != nil {
				return m.failRollback(backupFull, activeFull, err)
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
			rec.State = JournalStateRolledBack
			rec.Timestamp = time.Now().UTC()
			return m.writeJournal(rec)
		}
		// An interrupted first install has no previous runtime to restore and was
		// not verified before the crash. Preserve its files and fail closed.
		if activeExists {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "First installation was activated but not verified before interruption; runtime preserved for operator review", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Interrupted first installation left no active runtime; journal preserved for operator review", Recoverable: false, Err: ErrRecoveryAmbiguous}

	case JournalStateRollingBack:
		// Crashed during rollback: finish restoring backup -> active
		if backupExists {
			if activeExists {
				if err := os.RemoveAll(activeFull); err != nil {
					return err
				}
			}
			if err := os.Rename(backupFull, activeFull); err != nil {
				return m.failRollback(backupFull, activeFull, err)
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		}
		if stagingFull != "" {
			if err := os.RemoveAll(stagingFull); err != nil {
				return err
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		}
		rec.State = JournalStateRolledBack
		rec.Timestamp = time.Now().UTC()
		return m.writeJournal(rec)

	case JournalStateRolledBack:
		if stagingFull != "" {
			if err := os.RemoveAll(stagingFull); err != nil {
				return err
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		}
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

	_ = backupFi
	return nil
}

func (m *RecoveryManager) failRollback(backup, active string, err error) error {
	m.currentTx.State = JournalStateFailed
	if journalErr := m.writeJournal(m.currentTx); journalErr != nil {
		err = errors.Join(err, journalErr)
	}
	return &SafeError{
		Code: ErrCodeRollbackFailed,
		SafeMessage: fmt.Sprintf("CRITICAL: Error reconciliando y restaurando backup previo %s a %s: %v. Backup preservado. Comando manual: mv %s %s",
			backup, active, err, backup, active),
		Recoverable: false,
		Err:         ErrRollbackFailed,
	}
}

func (m *RecoveryManager) reconcileUnjournaled() error {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return err
	}

	var backups []string
	var stagings []string

	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(name, ".backup.") {
			backups = append(backups, filepath.Join(m.baseDir, name))
		} else if strings.HasPrefix(name, ".staging.") {
			stagings = append(stagings, filepath.Join(m.baseDir, name))
		}
	}

	if len(backups) == 0 {
		// Clean up any stale staging
		for _, s := range stagings {
			if err := os.RemoveAll(s); err != nil {
				return err
			}
		}
		return nil
	}

	// Sort backups by mod time descending
	sort.Slice(backups, func(i, j int) bool {
		fi1, err1 := os.Stat(backups[i])
		fi2, err2 := os.Stat(backups[j])
		if err1 == nil && err2 == nil {
			return fi1.ModTime().After(fi2.ModTime())
		}
		return backups[i] > backups[j]
	})

	for _, b := range backups {
		// Infer target from name: .backup.<target>.<txid>
		backupName := strings.TrimPrefix(filepath.Base(b), ".backup.")
		lastDot := strings.LastIndex(backupName, ".")
		if lastDot <= 0 || lastDot == len(backupName)-1 {
			continue
		}
		target := backupName[:lastDot]
		if !validTargetName(target) {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Unjournaled backup has an unsafe target name; backup preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		active := filepath.Join(m.baseDir, target)
		if _, err := m.validateConfinement(b); err != nil {
			return &SafeError{Code: ErrCodeRecoveryAmbiguous, SafeMessage: "Unjournaled backup path is unsafe; backup preserved", Recoverable: false, Err: ErrRecoveryAmbiguous}
		}
		if _, err := m.validateConfinement(active); err != nil {
			return err
		}

		activeFi, activeErr := os.Stat(active)
		if activeErr != nil && !os.IsNotExist(activeErr) {
			return fmt.Errorf("failed to inspect active runtime %s: %w", active, activeErr)
		}
		if activeErr != nil && os.IsNotExist(activeErr) {
			// Active is missing! Restore backup to active!
			if err := os.Rename(b, active); err != nil {
				return &SafeError{
					Code: ErrCodeRollbackFailed,
					SafeMessage: fmt.Sprintf("CRITICAL: Error restaurando backup previo %s a %s: %v. Backup preservado. Comando manual: mv %s %s",
						b, active, err, b, active),
					Recoverable: false,
					Err:         ErrRollbackFailed,
				}
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		} else if activeErr == nil && activeFi.IsDir() && dirIsEmpty(active) {
			// Active exists but is empty
			if err := os.Remove(active); err != nil {
				return err
			}
			if err := os.Rename(b, active); err != nil {
				return &SafeError{
					Code: ErrCodeRollbackFailed,
					SafeMessage: fmt.Sprintf("CRITICAL: Error restaurando backup previo %s a %s: %v. Backup preservado. Comando manual: mv %s %s",
						b, active, err, b, active),
					Recoverable: false,
					Err:         ErrRollbackFailed,
				}
			}
			if err := m.syncDirectory(m.baseDir); err != nil {
				return err
			}
		} else if activeErr == nil {
			// Active exists and has content! Ambiguous! Preserve backup!
			return &SafeError{
				Code: ErrCodeRecoveryAmbiguous,
				SafeMessage: fmt.Sprintf("Ambiguous recovery state: both active %s and backup %s exist with content. Backup preserved.",
					active, b),
				Recoverable: false,
				Err:         ErrRecoveryAmbiguous,
			}
		}
	}

	for _, s := range stagings {
		if err := os.RemoveAll(s); err != nil {
			return err
		}
	}

	return nil
}

func dirIsEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}
