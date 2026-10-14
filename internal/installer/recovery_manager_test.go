package installer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func createTestTree(t *testing.T, base string, files map[string]string) {
	t.Helper()
	for relPath, content := range files {
		full := filepath.Join(base, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("failed to create dir for %s: %v", relPath, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write test file %s: %v", relPath, err)
		}
	}
}

func verifyTestTree(t *testing.T, base string, expected map[string]string) {
	t.Helper()
	for relPath, expectedContent := range expected {
		full := filepath.Join(base, relPath)
		data, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("expected file %s to exist, but read failed: %v", relPath, err)
		}
		if string(data) != expectedContent {
			t.Fatalf("file %s content mismatch: got %q, want %q", relPath, string(data), expectedContent)
		}
	}
}

func TestRecoveryManager_HappyPath_InitialAndUpgrade(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	defer rm.ReleaseLock()

	// Initial installation of "python"
	stagingDir, err := rm.Begin(ctx, "python", "3.11.9", "hash1")
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	createTestTree(t, stagingDir, map[string]string{
		"bin/python3":          "python-binary-v1",
		"lib/python3.11/os.py": "print('v1')",
	})

	verifyFn := func(path string) error {
		if _, err := os.Stat(filepath.Join(path, "bin/python3")); err != nil {
			return errors.New("missing bin/python3 in verification")
		}
		return nil
	}

	if err := rm.CommitStaged(ctx, verifyFn); err != nil {
		t.Fatalf("CommitStaged failed: %v", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	verifyTestTree(t, activeDir, map[string]string{
		"bin/python3":          "python-binary-v1",
		"lib/python3.11/os.py": "print('v1')",
	})

	// Upgrade installation of "python" to v2
	stagingDir2, err := rm.Begin(ctx, "python", "3.11.10", "hash2")
	if err != nil {
		t.Fatalf("Begin upgrade failed: %v", err)
	}

	createTestTree(t, stagingDir2, map[string]string{
		"bin/python3":          "python-binary-v2",
		"lib/python3.11/os.py": "print('v2')",
	})

	if err := rm.CommitStaged(ctx, verifyFn); err != nil {
		t.Fatalf("CommitStaged upgrade failed: %v", err)
	}

	verifyTestTree(t, activeDir, map[string]string{
		"bin/python3":          "python-binary-v2",
		"lib/python3.11/os.py": "print('v2')",
	})

	// Check that no staging or backup directories remain
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read tmpDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name != "python" && name != "recovery.journal" && name != ".recovery.lock" {
			t.Fatalf("unexpected leftover directory or file: %s", name)
		}
	}
}

func TestRecoveryManager_CrashInBackingUp(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	// Create initial active "python"
	activeDir := filepath.Join(tmpDir, "python")
	createTestTree(t, activeDir, map[string]string{
		"bin/python3": "active-v1",
	})

	// Inject failure right after moving active to backup
	simulatedCrashErr := errors.New("simulated crash after rename active -> backup")
	rm.SetFaultInjector(&FaultInjector{
		AfterRename: func(from, to string) error {
			if filepath.Base(from) == "python" {
				return simulatedCrashErr
			}
			return nil
		},
	})

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}

	stagingDir, err := rm.Begin(ctx, "python", "3.11.10", "hash2")
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	createTestTree(t, stagingDir, map[string]string{
		"bin/python3": "staging-v2",
	})

	err = rm.CommitStaged(ctx, func(string) error { return nil })
	if !errors.Is(err, simulatedCrashErr) {
		t.Fatalf("expected simulatedCrashErr, got %v", err)
	}
	rm.ReleaseLock()

	// Simulate system restart / new process initializing RecoveryManager
	rmRecover, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create new RecoveryManager: %v", err)
	}

	if err := rmRecover.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock on restart failed: %v", err)
	}
	defer rmRecover.ReleaseLock()

	if err := rmRecover.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Verify active is restored to original v1
	verifyTestTree(t, activeDir, map[string]string{
		"bin/python3": "active-v1",
	})
}

func TestRecoveryManager_CrashInBackedUp(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	createTestTree(t, activeDir, map[string]string{
		"bin/python3": "active-v1",
	})

	// Inject error right before activating staging -> active
	simulatedCrashErr := errors.New("simulated crash before activating")
	rm.SetFaultInjector(&FaultInjector{
		BeforeStateChange: func(s JournalState) error {
			if s == JournalStateActivating {
				return simulatedCrashErr
			}
			return nil
		},
	})

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}

	stagingDir, err := rm.Begin(ctx, "python", "3.11.10", "hash2")
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	createTestTree(t, stagingDir, map[string]string{
		"bin/python3": "staging-v2",
	})

	err = rm.CommitStaged(ctx, func(string) error { return nil })
	if !errors.Is(err, simulatedCrashErr) {
		t.Fatalf("expected simulatedCrashErr, got %v", err)
	}
	rm.ReleaseLock()

	// Simulate restart
	rmRecover, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}
	if err := rmRecover.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rmRecover.ReleaseLock()

	if err := rmRecover.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Verify original active is restored
	verifyTestTree(t, activeDir, map[string]string{
		"bin/python3": "active-v1",
	})
}

func TestRecoveryManager_CrashInActivating(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	createTestTree(t, activeDir, map[string]string{
		"bin/python3": "active-v1",
	})

	// Inject crash right after staging -> active rename
	simulatedCrashErr := errors.New("simulated crash right after staging rename")
	rm.SetFaultInjector(&FaultInjector{
		AfterRename: func(from, to string) error {
			if filepath.Base(to) == "python" {
				return simulatedCrashErr
			}
			return nil
		},
	})

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}

	stagingDir, err := rm.Begin(ctx, "python", "3.11.10", "hash2")
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	createTestTree(t, stagingDir, map[string]string{
		"bin/python3": "staging-v2",
	})

	_ = rm.CommitStaged(ctx, func(string) error { return nil })
	rm.ReleaseLock()

	// Reconcile on restart: state was ACTIVATING/ACTIVATED with unverified active and valid backup.
	// Reconcile must safely roll back to backup because commit was never acknowledged!
	rmRecover, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}
	if err := rmRecover.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rmRecover.ReleaseLock()

	if err := rmRecover.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Incomplete/uncommitted activation must roll back to original backup
	verifyTestTree(t, activeDir, map[string]string{
		"bin/python3": "active-v1",
	})
}

func TestRecoveryManager_CrashInRollingBack(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	backupDir := filepath.Join(tmpDir, ".backup.python.tx_test")
	createTestTree(t, backupDir, map[string]string{
		"bin/python3": "backup-v1",
	})
	backupHash, err := rm.syncAndHashTree(backupDir)
	if err != nil {
		t.Fatal(err)
	}

	// Manually write journal record in ROLLING_BACK state
	rec := &JournalRecord{
		Version:      1,
		TxID:         "tx_test",
		State:        JournalStateRollingBack,
		Target:       "python",
		TargetVer:    "3.11.10",
		ActivePath:   "python",
		StagingPath:  ".staging.python.tx_test",
		BackupPath:   ".backup.python.tx_test",
		ExpectedHash: "somehash",
		HadActive:    true,
		BackupHash:   backupHash,
		Timestamp:    time.Now().UTC(),
	}
	if err := rm.writeJournal(rec); err != nil {
		t.Fatalf("failed to write mock journal: %v", err)
	}

	// Reconcile finishes rollback
	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rm.ReleaseLock()

	if err := rm.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	verifyTestTree(t, activeDir, map[string]string{
		"bin/python3": "backup-v1",
	})
}

func TestRecoveryManager_CorruptJournal_FailsClosedAndPreservesBackups(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	backupDir := filepath.Join(tmpDir, ".backup.python.tx_corrupt")
	createTestTree(t, activeDir, map[string]string{"bin/python3": "active-content"})
	createTestTree(t, backupDir, map[string]string{"bin/python3": "backup-content"})

	// Write garbage to recovery.journal
	journalPath := filepath.Join(tmpDir, "recovery.journal")
	if err := os.WriteFile(journalPath, []byte("NOT_VALID_JSON_OR_CORRUPT"), 0o600); err != nil {
		t.Fatalf("failed to write corrupt journal: %v", err)
	}

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rm.ReleaseLock()

	err = rm.Reconcile(ctx)
	if err == nil {
		t.Fatal("expected error on corrupt journal, got nil")
	}

	var safeErr *SafeError
	if !errors.As(err, &safeErr) {
		t.Fatalf("expected SafeError, got %T: %v", err, err)
	}
	if safeErr.Code != ErrCodeJournalCorrupt {
		t.Fatalf("expected ErrCodeJournalCorrupt, got %s", safeErr.Code)
	}

	// Invariant: Both active and backup MUST remain completely intact!
	verifyTestTree(t, activeDir, map[string]string{"bin/python3": "active-content"})
	verifyTestTree(t, backupDir, map[string]string{"bin/python3": "backup-content"})
}

func TestRecoveryManager_JournalWriteFailure_AbortsCleanly(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	createTestTree(t, activeDir, map[string]string{"bin/python3": "active-original"})

	simulatedIoErr := errors.New("simulated disk full on fsync")
	rm.SetFaultInjector(&FaultInjector{
		BeforeSync: func(path string) error {
			return simulatedIoErr
		},
	})

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rm.ReleaseLock()

	_, err = rm.Begin(ctx, "python", "3.11.10", "hash2")
	if !errors.Is(err, simulatedIoErr) {
		t.Fatalf("expected simulatedIoErr, got %v", err)
	}

	// Active must remain completely untouched
	verifyTestTree(t, activeDir, map[string]string{"bin/python3": "active-original"})
}

func TestRecoveryManager_RollbackFailure_PreservesBackupAndInstructs(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	createTestTree(t, activeDir, map[string]string{"bin/python3": "active-v1"})

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rm.ReleaseLock()

	stagingDir, err := rm.Begin(ctx, "python", "3.11.10", "hash2")
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	createTestTree(t, stagingDir, map[string]string{"bin/python3": "staging-v2"})

	// Inject error during rollback restoration
	simulatedRestoreErr := errors.New("simulated permission denied during rollback rename")
	rm.SetFaultInjector(&FaultInjector{
		BeforeRename: func(from, to string) error {
			// Trigger only when attempting to restore backup -> active
			if filepath.Base(to) == "python" && filepath.Base(from) != filepath.Base(stagingDir) {
				return simulatedRestoreErr
			}
			return nil
		},
	})

	// Fail post-activation verification to trigger rollback
	failingVerify := func(path string) error {
		if filepath.Base(path) == "python" {
			return errors.New("post-activation smoke test failed")
		}
		return nil
	}

	err = rm.CommitStaged(ctx, failingVerify)
	if err == nil {
		t.Fatal("expected rollback failure error, got nil")
	}

	var safeErr *SafeError
	if !errors.As(err, &safeErr) {
		t.Fatalf("expected SafeError, got %T: %v", err, err)
	}
	if safeErr.Code != ErrCodeRollbackFailed {
		t.Fatalf("expected ErrCodeRollbackFailed, got %s", safeErr.Code)
	}

	// Backup MUST NOT be removed!
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	backupFound := false
	for _, e := range entries {
		if len(e.Name()) > 8 && e.Name()[:8] == ".backup." {
			backupFound = true
			backupPath := filepath.Join(tmpDir, e.Name())
			verifyTestTree(t, backupPath, map[string]string{"bin/python3": "active-v1"})
		}
	}
	if !backupFound {
		t.Fatal("expected backup directory to be preserved, but none found")
	}
}

func TestRecoveryManager_AmbiguousState_PreservesBackup(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	// Ambiguous situation: active exists and non-empty, and orphan backup exists and non-empty
	activeDir := filepath.Join(tmpDir, "python")
	backupDir := filepath.Join(tmpDir, ".backup.python.tx_ambiguous")
	createTestTree(t, activeDir, map[string]string{"bin/python3": "active-data"})
	createTestTree(t, backupDir, map[string]string{"bin/python3": "backup-data"})

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rm.ReleaseLock()

	err = rm.Reconcile(ctx)
	if err == nil {
		t.Fatal("expected ErrRecoveryAmbiguous, got nil")
	}

	var safeErr *SafeError
	if !errors.As(err, &safeErr) {
		t.Fatalf("expected SafeError, got %T: %v", err, err)
	}
	if safeErr.Code != ErrCodeRecoveryAmbiguous {
		t.Fatalf("expected ErrCodeRecoveryAmbiguous, got %s", safeErr.Code)
	}

	// Invariant: Both must still exist!
	verifyTestTree(t, activeDir, map[string]string{"bin/python3": "active-data"})
	verifyTestTree(t, backupDir, map[string]string{"bin/python3": "backup-data"})
}

func TestRecoveryManager_SymlinkAndPathTraversalRejection(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rm.ReleaseLock()

	// 1. Path traversal via '..'
	_, err = rm.Begin(ctx, "../escaping_dir", "1.0", "hash")
	if !errors.Is(err, ErrSecurityEscapingSymlink) {
		t.Fatalf("expected ErrSecurityEscapingSymlink on '..', got: %v", err)
	}

	// 2. Symlink pointing outside baseDir
	externalDir := t.TempDir()
	escapingSymlink := filepath.Join(tmpDir, "symlink_escape")
	if err := os.Symlink(externalDir, escapingSymlink); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	_, err = rm.Begin(ctx, "symlink_escape/nested", "1.0", "hash")
	if !errors.Is(err, ErrSecurityEscapingSymlink) {
		t.Fatalf("expected ErrSecurityEscapingSymlink on symlink escape, got: %v", err)
	}
}

func TestRecoveryManager_LockProtection_ProcessAndThread(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm1, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create rm1: %v", err)
	}

	rm2, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create rm2: %v", err)
	}

	// In-process lock contention
	if err := rm1.AcquireLock(ctx); err != nil {
		t.Fatalf("rm1 failed to acquire lock: %v", err)
	}

	err = rm2.AcquireLock(ctx)
	if !errors.Is(err, ErrRecoveryLockHeld) {
		t.Fatalf("expected rm2 to fail with ErrRecoveryLockHeld, got %v", err)
	}

	rm1.ReleaseLock()

	// After release, rm2 must succeed
	if err := rm2.AcquireLock(ctx); err != nil {
		t.Fatalf("rm2 failed to acquire lock after release: %v", err)
	}
	rm2.ReleaseLock()

	// Concurrent goroutines attempting lock
	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rm, err := NewRecoveryManager(tmpDir)
			if err != nil {
				return
			}
			if err := rm.AcquireLock(ctx); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				rm.ReleaseLock()
			}
		}()
	}
	wg.Wait()

	if successCount == 0 {
		t.Fatal("expected at least one goroutine to acquire lock")
	}
}

func TestRecoveryManager_ReconcileIdempotence(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	createTestTree(t, activeDir, map[string]string{"bin/python3": "python-v1"})

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rm.ReleaseLock()

	// Calling Reconcile repeatedly on a stable state must succeed without errors
	for i := 0; i < 5; i++ {
		if err := rm.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile iteration %d failed: %v", i, err)
		}
		verifyTestTree(t, activeDir, map[string]string{"bin/python3": "python-v1"})
	}
}

func TestRecoveryManager_UnjournaledBackupFailsClosedWithoutIntegrityEvidence(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	rm, err := NewRecoveryManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create RecoveryManager: %v", err)
	}

	// Active is missing, but an unjournaled backup exists
	backupDir := filepath.Join(tmpDir, ".backup.python.legacy_tx")
	createTestTree(t, backupDir, map[string]string{"bin/python3": "recovered-binary"})

	if err := rm.AcquireLock(ctx); err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer rm.ReleaseLock()

	if err := rm.Reconcile(ctx); !errors.Is(err, ErrRecoveryAmbiguous) {
		t.Fatalf("Reconcile error = %v, want unjournaled backup rejected", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	if _, err := os.Stat(activeDir); !os.IsNotExist(err) {
		t.Fatalf("active runtime should remain absent, stat err=%v", err)
	}
	verifyTestTree(t, backupDir, map[string]string{"bin/python3": "recovered-binary"})
}

func TestRecoveryManagerRequiresLockAndReleaseIsIdempotent(t *testing.T) {
	rm, err := NewRecoveryManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rm.Begin(context.Background(), "python", "v1", "hash"); !errors.Is(err, ErrRecoveryLockNotHeld) {
		t.Fatalf("Begin without lock error = %v, want ErrRecoveryLockNotHeld", err)
	}
	if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrRecoveryLockNotHeld) {
		t.Fatalf("Reconcile without lock error = %v, want ErrRecoveryLockNotHeld", err)
	}
	// Releasing an unowned lock must be a safe no-op, not an Unlock panic.
	rm.ReleaseLock()
	rm.ReleaseLock()
}

func TestRecoveryManagerBeginRequiresPendingJournalReconciliation(t *testing.T) {
	base := t.TempDir()
	first, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Begin(context.Background(), "python", "v1", "hash"); err != nil {
		t.Fatal(err)
	}
	first.ReleaseLock()

	second, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer second.ReleaseLock()
	if _, err := second.Begin(context.Background(), "python", "v2", "hash2"); !errors.Is(err, ErrTransactionInProgress) {
		t.Fatalf("Begin with pending journal error = %v, want ErrTransactionInProgress", err)
	}
	if err := second.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Begin(context.Background(), "python", "v2", "hash2"); err != nil {
		t.Fatalf("Begin after reconciliation failed: %v", err)
	}
}

func TestRecoveryManagerRejectsSemanticallyCorruptJournalPaths(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	createTestTree(t, active, map[string]string{"marker": "preserve"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()

	record := &JournalRecord{
		Version: 1, TxID: "tx_1_1", State: JournalStateIdle, Target: "python",
		ActivePath: "python", StagingPath: "../", Timestamp: time.Now().UTC(),
	}
	record.RecordHash = record.computeHash()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rm.journalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("Reconcile error = %v, want ErrJournalCorrupt", err)
	}
	verifyTestTree(t, active, map[string]string{"marker": "preserve"})
}

func TestRecoveryManagerRejectsSymlinkedJournalWithoutTouchingTarget(t *testing.T) {
	base := t.TempDir()
	externalJournal := filepath.Join(t.TempDir(), "external-journal")
	if err := os.WriteFile(externalJournal, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalJournal, rm.journalPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("Reconcile error = %v, want ErrJournalCorrupt", err)
	}
	data, err := os.ReadFile(externalJournal)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("external journal target changed: data=%q err=%v", data, err)
	}
}

func TestRecoveryManagerRejectsSymlinkedLockFile(t *testing.T) {
	base := t.TempDir()
	externalLock := filepath.Join(t.TempDir(), "external-lock")
	if err := os.WriteFile(externalLock, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalLock, rm.lockPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := rm.AcquireLock(context.Background()); !errors.Is(err, ErrSecurityEscapingSymlink) {
		t.Fatalf("AcquireLock error = %v, want ErrSecurityEscapingSymlink", err)
	}
	data, err := os.ReadFile(externalLock)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("external lock target changed: data=%q err=%v", data, err)
	}
}

func TestRecoveryManagerAmbiguousBackupTransitionPreservesBoth(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	backup := filepath.Join(base, ".backup.python.tx_1_1")
	createTestTree(t, active, map[string]string{"version": "active"})
	createTestTree(t, backup, map[string]string{"version": "backup"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	backupHash, err := rm.syncAndHashTree(backup)
	if err != nil {
		t.Fatal(err)
	}
	record := &JournalRecord{
		Version: 1, TxID: "tx_1_1", State: JournalStateBackingUp, Target: "python",
		ActivePath: "python", StagingPath: ".staging.python.tx_1_1", BackupPath: ".backup.python.tx_1_1",
		HadActive: true, BackupHash: backupHash,
		Timestamp: time.Now().UTC(),
	}
	if err := rm.writeJournal(record); err != nil {
		t.Fatal(err)
	}
	if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrRecoveryAmbiguous) {
		t.Fatalf("Reconcile error = %v, want ErrRecoveryAmbiguous", err)
	}
	verifyTestTree(t, active, map[string]string{"version": "active"})
	verifyTestTree(t, backup, map[string]string{"version": "backup"})
}

func TestRecoveryManagerMissingBackupInBackedUpStateFailsClosed(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	createTestTree(t, active, map[string]string{"version": "unverified-active"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	backupHash := strings.Repeat("a", 64)
	record := &JournalRecord{
		Version: 1, TxID: "tx_1_1", State: JournalStateBackedUp, Target: "python",
		ActivePath: "python", StagingPath: ".staging.python.tx_1_1", BackupPath: ".backup.python.tx_1_1",
		HadActive: true, BackupHash: backupHash,
		Timestamp: time.Now().UTC(),
	}
	if err := rm.writeJournal(record); err != nil {
		t.Fatal(err)
	}
	if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrRecoveryAmbiguous) {
		t.Fatalf("Reconcile error = %v, want ErrRecoveryAmbiguous", err)
	}
	verifyTestTree(t, active, map[string]string{"version": "unverified-active"})
}

func TestRecoveryManagerRollingBackMissingBackupPreservesActiveAndJournal(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	createTestTree(t, active, map[string]string{"version": "unverified-new"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	prior := t.TempDir()
	createTestTree(t, prior, map[string]string{"version": "prior-valid"})
	priorHash, err := rm.syncAndHashTree(prior)
	if err != nil {
		t.Fatal(err)
	}
	rec := &JournalRecord{Version: 1, TxID: "tx_missing", State: JournalStateRollingBack, Target: "python", ActivePath: "python", StagingPath: ".staging.python.tx_missing", BackupPath: ".backup.python.tx_missing", HadActive: true, BackupHash: priorHash, Timestamp: time.Now().UTC()}
	if err := rm.writeJournal(rec); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrRecoveryAmbiguous) {
			t.Fatalf("reconcile %d error = %v, want fail-closed missing-backup error", i, err)
		}
		verifyTestTree(t, active, map[string]string{"version": "unverified-new"})
		journal, err := rm.readJournal()
		if err != nil || journal.State != JournalStateRollingBack {
			t.Fatalf("reconcile %d journal = %#v, err=%v; want ROLLING_BACK", i, journal, err)
		}
	}
}

func TestRecoveryManagerCorruptBackupPreservesActiveBackupAndJournal(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	backup := filepath.Join(base, ".backup.python.tx_corrupt")
	createTestTree(t, active, map[string]string{"version": "unverified-new"})
	createTestTree(t, backup, map[string]string{"version": "corrupt"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	valid := t.TempDir()
	createTestTree(t, valid, map[string]string{"version": "prior-valid"})
	priorHash, err := rm.syncAndHashTree(valid)
	if err != nil {
		t.Fatal(err)
	}
	stagingHash, err := rm.syncAndHashTree(active)
	if err != nil {
		t.Fatal(err)
	}
	rec := &JournalRecord{Version: 1, TxID: "tx_corrupt", State: JournalStateActivated, Target: "python", ActivePath: "python", StagingPath: ".staging.python.tx_corrupt", BackupPath: ".backup.python.tx_corrupt", HadActive: true, BackupHash: priorHash, StagingHash: stagingHash, Timestamp: time.Now().UTC()}
	if err := rm.writeJournal(rec); err != nil {
		t.Fatal(err)
	}
	if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrRecoveryAmbiguous) {
		t.Fatalf("Reconcile error = %v, want corrupt backup rejection", err)
	}
	verifyTestTree(t, active, map[string]string{"version": "unverified-new"})
	verifyTestTree(t, backup, map[string]string{"version": "corrupt"})
}

func TestRecoveryManagerQuarantinesActiveAndRestoresVerifiedBackup(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	backup := filepath.Join(base, ".backup.python.tx_restore")
	createTestTree(t, active, map[string]string{"version": "unverified-new"})
	createTestTree(t, backup, map[string]string{"version": "prior-valid"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	backupHash, err := rm.syncAndHashTree(backup)
	if err != nil {
		t.Fatal(err)
	}
	stagingHash, err := rm.syncAndHashTree(active)
	if err != nil {
		t.Fatal(err)
	}
	rec := &JournalRecord{Version: 1, TxID: "tx_restore", State: JournalStateActivated, Target: "python", ActivePath: "python", StagingPath: ".staging.python.tx_restore", BackupPath: ".backup.python.tx_restore", HadActive: true, BackupHash: backupHash, StagingHash: stagingHash, Timestamp: time.Now().UTC()}
	if err := rm.writeJournal(rec); err != nil {
		t.Fatal(err)
	}
	if err := rm.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	verifyTestTree(t, active, map[string]string{"version": "prior-valid"})
	verifyTestTree(t, filepath.Join(base, ".quarantine.python.tx_restore"), map[string]string{"version": "unverified-new"})
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("restored backup should have been moved to active, stat err=%v", err)
	}
	for i := 0; i < 2; i++ {
		if err := rm.Reconcile(context.Background()); err != nil {
			t.Fatalf("repeated reconcile %d: %v", i, err)
		}
	}
}

func TestRecoveryManagerInterruptedRestoreCanBeRetried(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	backup := filepath.Join(base, ".backup.python.tx_retry")
	createTestTree(t, active, map[string]string{"version": "unverified-new"})
	createTestTree(t, backup, map[string]string{"version": "prior-valid"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	backupHash, err := rm.syncAndHashTree(backup)
	if err != nil {
		t.Fatal(err)
	}
	rec := &JournalRecord{Version: 1, TxID: "tx_retry", State: JournalStateRollingBack, Target: "python", ActivePath: "python", StagingPath: ".staging.python.tx_retry", BackupPath: ".backup.python.tx_retry", HadActive: true, BackupHash: backupHash, Timestamp: time.Now().UTC()}
	if err := rm.writeJournal(rec); err != nil {
		t.Fatal(err)
	}
	restoreErr := errors.New("injected restore rename failure")
	rm.SetFaultInjector(&FaultInjector{BeforeRename: func(from, to string) error {
		if filepath.Base(from) == filepath.Base(backup) && filepath.Base(to) == filepath.Base(active) {
			return restoreErr
		}
		return nil
	}})
	if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("first Reconcile error = %v, want rollback-required error", err)
	}
	verifyTestTree(t, backup, map[string]string{"version": "prior-valid"})
	verifyTestTree(t, filepath.Join(base, ".quarantine.python.tx_retry"), map[string]string{"version": "unverified-new"})
	rm.SetFaultInjector(nil)
	if err := rm.Reconcile(context.Background()); err != nil {
		t.Fatalf("retry Reconcile: %v", err)
	}
	verifyTestTree(t, active, map[string]string{"version": "prior-valid"})
}

func TestRecoveryManagerFinalizedJournalDoesNotCertifyAlteredRuntime(t *testing.T) {
	base := t.TempDir()
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	staging, err := rm.Begin(context.Background(), "python", "v1", "release-hash")
	if err != nil {
		t.Fatal(err)
	}
	createTestTree(t, staging, map[string]string{"bin/python": "v1"})
	if err := rm.CommitStaged(context.Background(), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	createTestTree(t, filepath.Join(base, "python"), map[string]string{"tampered": "yes"})
	if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrRecoveryAmbiguous) {
		t.Fatalf("Reconcile error = %v, want altered committed runtime rejected", err)
	}
	verifyTestTree(t, filepath.Join(base, "python"), map[string]string{"tampered": "yes"})
}

func TestRecoveryManagerCommittedJournalWithMissingRuntimeFailsClosed(t *testing.T) {
	base := t.TempDir()
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	fixture := t.TempDir()
	createTestTree(t, fixture, map[string]string{"bin/python": "committed"})
	hash, err := rm.syncAndHashTree(fixture)
	if err != nil {
		t.Fatal(err)
	}
	rec := &JournalRecord{Version: 1, TxID: "tx_absent", State: JournalStateCommitted, Target: "python", ActivePath: "python", StagingPath: ".staging.python.tx_absent", StagingHash: hash, ActiveHash: hash, Timestamp: time.Now().UTC()}
	if err := rm.writeJournal(rec); err != nil {
		t.Fatal(err)
	}
	if err := rm.Reconcile(context.Background()); !errors.Is(err, ErrRecoveryAmbiguous) {
		t.Fatalf("Reconcile error = %v, want missing committed runtime rejected", err)
	}
	journal, err := rm.readJournal()
	if err != nil || journal.State != JournalStateCommitted {
		t.Fatalf("journal changed despite missing runtime: %#v, err=%v", journal, err)
	}
}

func TestRecoveryManagerPayloadSyncFailurePreventsActivation(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	createTestTree(t, active, map[string]string{"version": "prior"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	staging, err := rm.Begin(context.Background(), "python", "v2", "release-hash")
	if err != nil {
		t.Fatal(err)
	}
	createTestTree(t, staging, map[string]string{"version": "new"})
	syncErr := errors.New("injected payload directory fsync failure")
	rm.syncDirectory = func(path string) error {
		if path == staging {
			return syncErr
		}
		return syncDir(path)
	}
	if err := rm.CommitStaged(context.Background(), func(string) error { return nil }); !errors.Is(err, syncErr) {
		t.Fatalf("CommitStaged error = %v, want payload sync failure", err)
	}
	verifyTestTree(t, active, map[string]string{"version": "prior"})
	if _, err := os.Stat(filepath.Join(base, ".backup.python."+rm.currentTx.TxID)); !os.IsNotExist(err) {
		t.Fatalf("backup created before staging durability check, stat err=%v", err)
	}
}

func TestRecoveryManagerParentSyncFailureAfterBackupRenameRecoversPriorRuntime(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	createTestTree(t, active, map[string]string{"version": "prior"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	staging, err := rm.Begin(context.Background(), "python", "v2", "release-hash")
	if err != nil {
		t.Fatal(err)
	}
	createTestTree(t, staging, map[string]string{"version": "new"})
	syncErr := errors.New("injected parent fsync failure after active-to-backup rename")
	baseSyncs := 0
	rm.syncDirectory = func(path string) error {
		if path == rm.baseDir {
			baseSyncs++
			if baseSyncs == 3 {
				return syncErr
			}
		}
		return syncDir(path)
	}
	if err := rm.CommitStaged(context.Background(), func(string) error { return nil }); !errors.Is(err, syncErr) {
		t.Fatalf("CommitStaged error = %v, want parent sync failure", err)
	}
	backup := filepath.Join(base, ".backup.python."+rm.currentTx.TxID)
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Fatalf("active should be absent at the interrupted rename boundary, stat err=%v", err)
	}
	verifyTestTree(t, backup, map[string]string{"version": "prior"})
	rm.syncDirectory = syncDir
	if err := rm.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after sync failure: %v", err)
	}
	verifyTestTree(t, active, map[string]string{"version": "prior"})
}

func TestRecoveryManagerDirectorySyncFailureAborts(t *testing.T) {
	rm, err := NewRecoveryManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	syncErr := errors.New("injected directory fsync failure")
	rm.syncDirectory = func(string) error { return syncErr }
	if _, err := rm.Begin(context.Background(), "python", "v1", "hash"); !errors.Is(err, syncErr) {
		t.Fatalf("Begin error = %v, want directory fsync error", err)
	}
}

func TestRecoveryManagerSubprocessHelper(t *testing.T) {
	mode := os.Getenv("GEOCAM_RECOVERY_HELPER_MODE")
	if mode == "" {
		return
	}
	base := os.Getenv("GEOCAM_RECOVERY_HELPER_BASE")
	marker := os.Getenv("GEOCAM_RECOVERY_HELPER_MARKER")
	release := os.Getenv("GEOCAM_RECOVERY_HELPER_RELEASE")
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()
	if mode == "lock" {
		if err := os.WriteFile(marker, []byte("locked"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := os.Stat(release); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if mode == "crash" {
		staging, err := rm.Begin(context.Background(), "python", "v2", "hash-v2")
		if err != nil {
			t.Fatal(err)
		}
		createTestTree(t, staging, map[string]string{"bin/python": "new"})
		rm.SetFaultInjector(&FaultInjector{AfterRename: func(from, to string) error {
			if to == filepath.Join(rm.baseDir, "python") {
				if err := os.WriteFile(marker, []byte("activated"), 0o600); err != nil {
					return err
				}
				select {}
			}
			return nil
		}})
		if err := rm.CommitStaged(context.Background(), func(string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		t.Fatal("crash helper unexpectedly returned")
	}
	t.Fatalf("unknown helper mode %q", mode)
}

func TestRecoveryManagerCrossProcessLock(t *testing.T) {
	base := t.TempDir()
	marker := filepath.Join(t.TempDir(), "locked")
	release := filepath.Join(t.TempDir(), "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryManagerSubprocessHelper$")
	cmd.Env = append(os.Environ(), "GEOCAM_RECOVERY_HELPER_MODE=lock", "GEOCAM_RECOVERY_HELPER_BASE="+base,
		"GEOCAM_RECOVERY_HELPER_MARKER="+marker, "GEOCAM_RECOVERY_HELPER_RELEASE="+release)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(release, []byte("release"), 0o600)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	waitForMarker(t, marker, cmd)
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); !errors.Is(err, ErrRecoveryLockHeld) {
		t.Fatalf("second process lock error = %v, want ErrRecoveryLockHeld", err)
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("lock helper failed: %v", err)
	}
}

func TestRecoveryManagerSIGKILLDuringActivationRestoresPreviousRuntime(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	createTestTree(t, active, map[string]string{"bin/python": "old"})
	marker := filepath.Join(t.TempDir(), "activated")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryManagerSubprocessHelper$")
	cmd.Env = append(os.Environ(), "GEOCAM_RECOVERY_HELPER_MODE=crash", "GEOCAM_RECOVERY_HELPER_BASE="+base,
		"GEOCAM_RECOVERY_HELPER_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	waitForMarker(t, marker, cmd)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("failed to terminate activation process: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed helper unexpectedly exited successfully")
	}

	recoverer, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverer.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer recoverer.ReleaseLock()
	if err := recoverer.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile after process kill failed: %v", err)
	}
	verifyTestTree(t, active, map[string]string{"bin/python": "old"})
}

func waitForMarker(t *testing.T, marker string, cmd *exec.Cmd) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if cmd.ProcessState != nil {
			t.Fatalf("helper exited before writing marker: %v", cmd.ProcessState)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for helper process marker")
}

func TestRecoveryManagerActivatingMissingBackupFailsClosedAndPreservesActive(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	createTestTree(t, active, map[string]string{"version": "unverified-active"})
	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()

	prior := t.TempDir()
	createTestTree(t, prior, map[string]string{"version": "prior-valid"})
	priorHash, err := rm.syncAndHashTree(prior)
	if err != nil {
		t.Fatal(err)
	}
	activeHash, err := rm.syncAndHashTree(active)
	if err != nil {
		t.Fatal(err)
	}

	rec := &JournalRecord{
		Version:     1,
		TxID:        "tx_act_missing",
		State:       JournalStateActivating,
		Target:      "python",
		ActivePath:  "python",
		StagingPath: ".staging.python.tx_act_missing",
		BackupPath:  ".backup.python.tx_act_missing",
		HadActive:   true,
		BackupHash:  priorHash,
		StagingHash: activeHash,
		Timestamp:   time.Now().UTC(),
	}
	if err := rm.writeJournal(rec); err != nil {
		t.Fatal(err)
	}

	err = rm.Reconcile(context.Background())
	if !errors.Is(err, ErrRecoveryAmbiguous) {
		t.Fatalf("Reconcile error = %v, want ErrRecoveryAmbiguous", err)
	}
	verifyTestTree(t, active, map[string]string{"version": "unverified-active"})
	journal, err := rm.readJournal()
	if err != nil || journal.State != JournalStateActivating {
		t.Fatalf("journal should remain in ACTIVATING state, got state=%v, err=%v", journal.State, err)
	}
}

func TestRecoveryManagerEmptyBackupDirectoryFailsClosedAndPreservesActive(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	backup := filepath.Join(base, ".backup.python.tx_empty")
	createTestTree(t, active, map[string]string{"version": "unverified-active"})
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}

	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()

	prior := t.TempDir()
	createTestTree(t, prior, map[string]string{"version": "prior-valid"})
	priorHash, err := rm.syncAndHashTree(prior)
	if err != nil {
		t.Fatal(err)
	}
	activeHash, err := rm.syncAndHashTree(active)
	if err != nil {
		t.Fatal(err)
	}

	rec := &JournalRecord{
		Version:     1,
		TxID:        "tx_empty",
		State:       JournalStateActivated,
		Target:      "python",
		ActivePath:  "python",
		StagingPath: ".staging.python.tx_empty",
		BackupPath:  ".backup.python.tx_empty",
		HadActive:   true,
		BackupHash:  priorHash,
		StagingHash: activeHash,
		Timestamp:   time.Now().UTC(),
	}
	if err := rm.writeJournal(rec); err != nil {
		t.Fatal(err)
	}

	err = rm.Reconcile(context.Background())
	if !errors.Is(err, ErrRecoveryAmbiguous) {
		t.Fatalf("Reconcile error = %v, want ErrRecoveryAmbiguous on empty backup", err)
	}
	verifyTestTree(t, active, map[string]string{"version": "unverified-active"})
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		t.Fatal("empty backup directory must be preserved on disk")
	}
}

func TestRecoveryManagerCrashBetweenRenamesRestoresActiveCleanly(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "python")
	backup := filepath.Join(base, ".backup.python.tx_between")
	staging := filepath.Join(base, ".staging.python.tx_between")
	createTestTree(t, backup, map[string]string{"version": "original-prior"})
	createTestTree(t, staging, map[string]string{"version": "staged-new"})

	rm, err := NewRecoveryManager(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rm.ReleaseLock()

	backupHash, err := rm.syncAndHashTree(backup)
	if err != nil {
		t.Fatal(err)
	}
	stagingHash, err := rm.syncAndHashTree(staging)
	if err != nil {
		t.Fatal(err)
	}

	rec := &JournalRecord{
		Version:     1,
		TxID:        "tx_between",
		State:       JournalStateBackedUp,
		Target:      "python",
		ActivePath:  "python",
		StagingPath: ".staging.python.tx_between",
		BackupPath:  ".backup.python.tx_between",
		HadActive:   true,
		BackupHash:  backupHash,
		StagingHash: stagingHash,
		Timestamp:   time.Now().UTC(),
	}
	if err := rm.writeJournal(rec); err != nil {
		t.Fatal(err)
	}

	if err := rm.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile crash between renames failed: %v", err)
	}
	verifyTestTree(t, active, map[string]string{"version": "original-prior"})
	journal, err := rm.readJournal()
	if err != nil || journal.State != JournalStateRolledBack {
		t.Fatalf("journal should transition to ROLLED_BACK, got state=%v, err=%v", journal.State, err)
	}
}

func TestRecoveryManagerJournalRecordRejectsInconsistentCombinations(t *testing.T) {
	now := time.Now().UTC()
	// 1. BACKING_UP without BackupPath
	r1 := &JournalRecord{
		Version:     1,
		TxID:        "tx_1",
		State:       JournalStateBackingUp,
		Target:      "python",
		ActivePath:  "python",
		StagingPath: ".staging.python.tx_1",
		BackupPath:  "",
		Timestamp:   now,
	}
	r1.RecordHash = r1.computeHash()
	if r1.isValid() {
		t.Fatal("expected BACKING_UP without BackupPath to be invalid")
	}

	// 2. PREPARING with BackupPath
	r2 := &JournalRecord{
		Version:     1,
		TxID:        "tx_2",
		State:       JournalStatePreparing,
		Target:      "python",
		ActivePath:  "python",
		StagingPath: ".staging.python.tx_2",
		BackupPath:  ".backup.python.tx_2",
		Timestamp:   now,
	}
	r2.RecordHash = r2.computeHash()
	if r2.isValid() {
		t.Fatal("expected PREPARING with BackupPath to be invalid")
	}

	// 3. Absolute path in StagingPath
	r3 := &JournalRecord{
		Version:     1,
		TxID:        "tx_3",
		State:       JournalStatePreparing,
		Target:      "python",
		ActivePath:  "python",
		StagingPath: "/tmp/staging",
		Timestamp:   now,
	}
	r3.RecordHash = r3.computeHash()
	if r3.isValid() {
		t.Fatal("expected absolute StagingPath to be invalid")
	}

	// 4. TargetVer with path traversal
	r4 := &JournalRecord{
		Version:     1,
		TxID:        "tx_4",
		State:       JournalStatePreparing,
		Target:      "python",
		TargetVer:   "../malicious",
		ActivePath:  "python",
		StagingPath: ".staging.python.tx_4",
		Timestamp:   now,
	}
	r4.RecordHash = r4.computeHash()
	if r4.isValid() {
		t.Fatal("expected TargetVer with traversal to be invalid")
	}
}
