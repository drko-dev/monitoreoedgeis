package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

	err = rm.CommitStaged(ctx, nil)
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

	err = rm.CommitStaged(ctx, nil)
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

	_ = rm.CommitStaged(ctx, nil)
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

func TestRecoveryManager_UnjournaledReconcile_RecoversMissingActive(t *testing.T) {
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

	if err := rm.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	activeDir := filepath.Join(tmpDir, "python")
	verifyTestTree(t, activeDir, map[string]string{"bin/python3": "recovered-binary"})
}
