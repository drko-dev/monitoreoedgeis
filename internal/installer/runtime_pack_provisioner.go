package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ModelRedistributionApproved is a release gate, not an installer option.
	ModelRedistributionApproved    = false
	DefaultRuntimePackManifestName = "runtime-manifest.json"
	DefaultRuntimePackTargetName   = "full-edge-runtime"
)

var (
	ErrRuntimePackProvisioningDisabled = errors.New("transactional Runtime Pack provisioning is opt-in and disabled")
	ErrRuntimePackManifestPinRequired  = errors.New("a trusted Runtime Pack manifest SHA-256 pin is required")
	ErrRuntimePackManifestPinMismatch  = errors.New("Runtime Pack manifest does not match the trusted SHA-256 pin")
	ErrRuntimePackUnlistedArtifact     = errors.New("Runtime Pack contains an artifact not declared by its manifest")
	ErrRuntimePackModelGovernance      = errors.New("model artifacts are blocked because redistribution is not approved")
)

// RuntimePackProvisionerOptions binds a local pack to a trusted manifest digest
// supplied by the embedding release. The default/product wiring remains opt-in.
type RuntimePackProvisionerOptions struct {
	Enabled                bool
	PackDir                string
	DataDir                string
	TargetName             string
	ManifestName           string
	ExpectedManifestSHA256 string
	TargetOS               string
	TargetArch             string
}

// RuntimePackProvisioningResult reports transactional integrity only. A
// successful activation is deliberately not a readiness certification.
type RuntimePackProvisioningResult struct {
	State                    string
	RuntimeVersion           string
	ManifestSHA256           string
	TransactionCommitted     bool
	AlreadyActive            bool
	RuntimeIntegrityVerified bool
	PinnedReleaseVerified    bool
	RuntimeReady             bool
	InferenceReady           bool
	ErrorCode                string
}

// TransactionalRuntimePackProvisioner validates and copies only manifest-
// declared artifacts into H6A's persistent staging area before activation.
// It does not run Python, FFmpeg, the worker, or inference.
type TransactionalRuntimePackProvisioner struct {
	options                RuntimePackProvisionerOptions
	recoveryManagerFactory func(string) (*RecoveryManager, error)
	// Opt-in final authorization check for signed releases. Runs once more
	// immediately before acknowledging an already-active pack or accepting
	// staged bytes for CommitStaged; legacy digest-only flows are unchanged.
	preCommitVerify func(context.Context) error
	// Optional installed-tree verification after transactional journal
	// reconciliation. Signed backends must not depend on source media to
	// recover a sealed pack already installed for offline use.
	preRecoverVerify func(context.Context) error
}

func NewTransactionalRuntimePackProvisioner(options RuntimePackProvisionerOptions) (*TransactionalRuntimePackProvisioner, error) {
	if options.TargetName == "" {
		options.TargetName = DefaultRuntimePackTargetName
	}
	if options.ManifestName == "" {
		options.ManifestName = DefaultRuntimePackManifestName
	}
	if !validTargetName(options.TargetName) {
		return nil, fmt.Errorf("invalid Runtime Pack target name %q", options.TargetName)
	}
	if err := validateRelativePath(options.ManifestName, "runtime manifest"); err != nil {
		return nil, err
	}
	if options.PackDir == "" || options.DataDir == "" {
		return nil, errors.New("Runtime Pack source and persistent data directory are required")
	}
	if options.TargetOS == "" || options.TargetArch == "" {
		return nil, errors.New("Runtime Pack target platform and architecture are required")
	}
	if options.ExpectedManifestSHA256 != "" && !isSHA256(options.ExpectedManifestSHA256) {
		return nil, errors.New("expected Runtime Pack manifest digest must be a SHA-256 hex digest")
	}
	packDir, err := filepath.Abs(filepath.Clean(options.PackDir))
	if err != nil {
		return nil, err
	}
	dataDir, err := filepath.Abs(filepath.Clean(options.DataDir))
	if err != nil {
		return nil, err
	}
	options.PackDir = packDir
	options.DataDir = dataDir
	return &TransactionalRuntimePackProvisioner{options: options, recoveryManagerFactory: NewRecoveryManager}, nil
}

// Prepare verifies the pinned source pack before acquiring the recovery lock,
// reconciles previous transactions, stages an allow-listed copy, and commits it
// through RecoveryManager. Disabled mode performs no filesystem mutations.
func (p *TransactionalRuntimePackProvisioner) Prepare(ctx context.Context) (RuntimePackProvisioningResult, error) {
	result := RuntimePackProvisioningResult{State: "CHECKING", RuntimeReady: false, InferenceReady: false}
	if p == nil || !p.options.Enabled {
		result.State, result.ErrorCode = "DISABLED", "RUNTIME_PACK_PROVISIONING_DISABLED"
		return result, ErrRuntimePackProvisioningDisabled
	}
	if err := ctx.Err(); err != nil {
		result.State, result.ErrorCode = "CANCELLED", "PREPARATION_CANCELLED"
		return result, err
	}
	if !isSHA256(p.options.ExpectedManifestSHA256) {
		result.State, result.ErrorCode = "FAILED", "MANIFEST_PIN_REQUIRED"
		return result, ErrRuntimePackManifestPinRequired
	}
	packRoot, err := canonicalDirectory(p.options.PackDir)
	if err != nil {
		return failedPackResult(result, "PACK_SOURCE_INVALID", err)
	}
	if err := ensureDisjointRuntimePaths(packRoot, p.options.DataDir); err != nil {
		return failedPackResult(result, "PACK_PATH_OVERLAP", err)
	}
	preflight, err := inspectRuntimePack(packRoot, p.options.ManifestName, p.options.ExpectedManifestSHA256, p.options.TargetOS, p.options.TargetArch)
	if err != nil {
		return failedPackResult(result, packErrorCode(err), err)
	}
	result.RuntimeVersion = preflight.manifest.RuntimeVersion
	result.ManifestSHA256 = preflight.manifestSHA256

	manager, err := p.newRecoveryManager(p.options.DataDir)
	if err != nil {
		return failedPackResult(result, "RECOVERY_MANAGER_INIT_FAILED", err)
	}
	if err := manager.AcquireLock(ctx); err != nil {
		return failedPackResult(result, "RECOVERY_LOCK_FAILED", err)
	}
	defer manager.ReleaseLock()
	if err := manager.Reconcile(ctx); err != nil {
		result.State, result.ErrorCode = "RECOVERY_REQUIRED", "RUNTIME_RECOVERY_REQUIRED"
		return result, err
	}

	activePath := filepath.Join(p.options.DataDir, p.options.TargetName)
	if _, err := os.Lstat(activePath); err == nil {
		active, verifyErr := inspectRuntimePack(activePath, p.options.ManifestName, p.options.ExpectedManifestSHA256, p.options.TargetOS, p.options.TargetArch)
		if verifyErr == nil && active.manifestSHA256 == preflight.manifestSHA256 {
			if p.preCommitVerify != nil {
				if err := p.preCommitVerify(ctx); err != nil {
					return failedPackResult(result, "SIGNED_MANIFEST_REJECTED", err)
				}
			}
			result.State = "COMMITTED_UNCERTIFIED"
			result.TransactionCommitted = true
			result.AlreadyActive = true
			result.RuntimeIntegrityVerified = true
			result.PinnedReleaseVerified = true
			return result, nil
		}
		// H6A has already reconciled and verified the installed tree against its
		// transaction evidence. A different, internally consistent release may
		// therefore be upgraded; an altered or malformed one remains fail-closed.
		if _, oldErr := inspectRuntimePackSelfConsistent(activePath, p.options.ManifestName, p.options.TargetOS, p.options.TargetArch); oldErr != nil {
			return failedPackResult(result, "ACTIVE_RUNTIME_UNVERIFIED", errors.Join(errors.New("active runtime is neither the requested pack nor a valid prior release"), verifyErr, oldErr))
		}
	} else if !os.IsNotExist(err) {
		return failedPackResult(result, "ACTIVE_RUNTIME_INSPECTION_FAILED", err)
	}

	stagingPath, err := manager.Begin(ctx, p.options.TargetName, preflight.manifest.RuntimeVersion, preflight.manifestSHA256)
	if err != nil {
		return failedPackResult(result, "RUNTIME_TRANSACTION_BEGIN_FAILED", err)
	}
	result.State = "STAGING"
	if err := stageRuntimePack(packRoot, stagingPath, p.options.ManifestName, preflight.manifest); err != nil {
		result.State, result.ErrorCode = "RECOVERY_REQUIRED", "RUNTIME_STAGING_FAILED"
		return result, err
	}
	if err := manager.CommitStaged(ctx, func(path string) error {
		staged, err := inspectRuntimePack(path, p.options.ManifestName, p.options.ExpectedManifestSHA256, p.options.TargetOS, p.options.TargetArch)
		if err != nil {
			return err
		}
		if staged.manifestSHA256 != preflight.manifestSHA256 || staged.manifest.RuntimeVersion != preflight.manifest.RuntimeVersion {
			return ErrRuntimePackManifestPinMismatch
		}
		// The selected signer may have been revoked during file staging.
		// Reject BEFORE RecoveryManager renames the staged bytes to active.
		if p.preCommitVerify != nil {
			if err := p.preCommitVerify(ctx); err != nil {
				return fmt.Errorf("signed Runtime Pack pre-commit authorization: %w", err)
			}
		}
		return nil
	}); err != nil {
		result.State, result.ErrorCode = "RECOVERY_REQUIRED", "RUNTIME_ACTIVATION_UNCERTAIN"
		return result, err
	}
	result.State = "COMMITTED_UNCERTIFIED"
	result.TransactionCommitted = true
	result.RuntimeIntegrityVerified = true
	result.PinnedReleaseVerified = true
	return result, nil
}

// Recover only reconciles H6A's persistent transaction. The returned integrity
// flag comes from the pinned pack manifest and is never readiness evidence.
func (p *TransactionalRuntimePackProvisioner) Recover(ctx context.Context) (RuntimePackProvisioningResult, error) {
	result := RuntimePackProvisioningResult{State: "RECOVERY_REQUIRED", RuntimeReady: false, InferenceReady: false}
	if p == nil || !p.options.Enabled {
		result.State, result.ErrorCode = "DISABLED", "RUNTIME_PACK_PROVISIONING_DISABLED"
		return result, ErrRuntimePackProvisioningDisabled
	}
	if !isSHA256(p.options.ExpectedManifestSHA256) {
		result.ErrorCode = "MANIFEST_PIN_REQUIRED"
		return result, ErrRuntimePackManifestPinRequired
	}
	manager, err := p.newRecoveryManager(p.options.DataDir)
	if err != nil {
		result.ErrorCode = "RECOVERY_MANAGER_INIT_FAILED"
		return result, err
	}
	if err := manager.AcquireLock(ctx); err != nil {
		result.ErrorCode = "RECOVERY_LOCK_FAILED"
		return result, err
	}
	defer manager.ReleaseLock()
	if err := manager.Reconcile(ctx); err != nil {
		result.ErrorCode = "RUNTIME_RECOVERY_REQUIRED"
		return result, err
	}
	// A clean journal with no active runtime is a first install, not a
	// rejected one: there are no installed bytes to authenticate yet.
	if _, err := os.Lstat(filepath.Join(p.options.DataDir, p.options.TargetName)); errors.Is(err, os.ErrNotExist) {
		result.State = "NOT_PREPARED"
		return result, nil
	}
	verification := p.preRecoverVerify
	if verification == nil {
		verification = p.preCommitVerify
	}
	if verification != nil {
		if err := verification(ctx); err != nil {
			result.ErrorCode = "SIGNED_MANIFEST_REJECTED"
			return result, err
		}
	}
	activePath := filepath.Join(p.options.DataDir, p.options.TargetName)
	// preRecoverVerify (signed backends) already ran the full pinned
	// inspection of the active tree; do not re-hash ~1 GB a second time.
	if p.preRecoverVerify != nil {
		data, err := loadPinnedRuntimeManifestBytes(filepath.Join(activePath, filepath.FromSlash(p.options.ManifestName)), p.options.ExpectedManifestSHA256)
		var manifest RuntimeManifest
		if err == nil {
			err = json.Unmarshal(data, &manifest)
		}
		if err != nil {
			result.ErrorCode = "SIGNED_MANIFEST_REJECTED"
			return result, err
		}
		result.State = "COMMITTED_UNCERTIFIED"
		result.TransactionCommitted = true
		result.RuntimeIntegrityVerified = true
		result.PinnedReleaseVerified = true
		result.RuntimeVersion = manifest.RuntimeVersion
		result.ManifestSHA256 = strings.ToLower(p.options.ExpectedManifestSHA256)
		return result, nil
	}
	active, err := inspectRuntimePack(activePath, p.options.ManifestName, p.options.ExpectedManifestSHA256, p.options.TargetOS, p.options.TargetArch)
	if err == nil {
		result.State = "COMMITTED_UNCERTIFIED"
		result.TransactionCommitted = true
		result.RuntimeIntegrityVerified = true
		result.PinnedReleaseVerified = true
		result.RuntimeVersion = active.manifest.RuntimeVersion
		result.ManifestSHA256 = active.manifestSHA256
		return result, nil
	}
	prior, priorErr := inspectRuntimePackSelfConsistent(activePath, p.options.ManifestName, p.options.TargetOS, p.options.TargetArch)
	if priorErr != nil {
		result.State, result.ErrorCode = "RECOVERED_UNCERTIFIED", "ACTIVE_RUNTIME_NOT_VERIFIED"
		return result, errors.Join(err, priorErr)
	}
	result.State = "ROLLED_BACK_UNCERTIFIED"
	result.RuntimeVersion = prior.manifest.RuntimeVersion
	result.ManifestSHA256 = prior.manifestSHA256
	result.RuntimeIntegrityVerified = true
	result.PinnedReleaseVerified = false
	return result, nil
}

func (p *TransactionalRuntimePackProvisioner) newRecoveryManager(path string) (*RecoveryManager, error) {
	if p.recoveryManagerFactory != nil {
		return p.recoveryManagerFactory(path)
	}
	return NewRecoveryManager(path)
}

type inspectedRuntimePack struct {
	manifest       *RuntimeManifest
	manifestSHA256 string
}

func inspectRuntimePack(root, manifestName, expectedManifestSHA256, targetOS, targetArch string) (inspectedRuntimePack, error) {
	var inspected inspectedRuntimePack
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		return inspected, err
	}
	manifestPath := filepath.Join(canonicalRoot, filepath.FromSlash(manifestName))
	if err := requireRegularPath(canonicalRoot, manifestPath); err != nil {
		return inspected, err
	}
	digest, err := hashFileSHA256(manifestPath)
	if err != nil {
		return inspected, fmt.Errorf("hash Runtime Pack manifest: %w", err)
	}
	if !strings.EqualFold(digest, expectedManifestSHA256) {
		return inspected, ErrRuntimePackManifestPinMismatch
	}
	manifest, err := LoadRuntimeManifest(manifestPath)
	if err != nil {
		return inspected, fmt.Errorf("load Runtime Pack manifest: %w", err)
	}
	if manifest.ManifestType != ManifestTypeRelease {
		return inspected, ErrRuntimePackNotRelease
	}
	if err := manifest.ValidateCompatibility(targetOS, targetArch); err != nil {
		return inspected, err
	}
	if err := rejectModelArtifacts(manifest.allArtifacts()); err != nil {
		return inspected, err
	}
	if _, err := manifest.ValidateArtifacts(canonicalRoot); err != nil {
		return inspected, err
	}
	if err := validatePackFileSet(canonicalRoot, manifestName, manifest.allArtifacts()); err != nil {
		return inspected, err
	}
	if _, err := ResolveRuntimePackMedia(manifest, canonicalRoot, targetOS, targetArch); err != nil {
		return inspected, err
	}
	// Non-executing contract check only. Persistent model/state paths must be
	// provided independently before any real Worker launch is authorized.
	modelsDir := filepath.Join(filepath.Dir(canonicalRoot), ".models-not-shipped")
	stateDir := filepath.Join(filepath.Dir(canonicalRoot), ".worker-state-not-provisioned")
	if _, err := BuildRuntimePackWorkerLaunchSpec(manifest, canonicalRoot, modelsDir, stateDir, targetOS, targetArch); err != nil {
		return inspected, err
	}
	return inspectedRuntimePack{manifest: manifest, manifestSHA256: digest}, nil
}

func inspectRuntimePackSelfConsistent(root, manifestName, targetOS, targetArch string) (inspectedRuntimePack, error) {
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		return inspectedRuntimePack{}, err
	}
	manifestPath := filepath.Join(canonicalRoot, filepath.FromSlash(manifestName))
	if err := requireRegularPath(canonicalRoot, manifestPath); err != nil {
		return inspectedRuntimePack{}, err
	}
	digest, err := hashFileSHA256(manifestPath)
	if err != nil {
		return inspectedRuntimePack{}, err
	}
	return inspectRuntimePack(canonicalRoot, manifestName, digest, targetOS, targetArch)
}

func stageRuntimePack(sourceRoot, stagingRoot, manifestName string, manifest *RuntimeManifest) error {
	paths := make([]string, 0, len(manifest.allArtifacts())+1)
	paths = append(paths, manifestName)
	for _, artifact := range manifest.allArtifacts() {
		paths = append(paths, artifact.RelativePath)
	}
	for _, relative := range paths {
		if err := copyVerifiedPackFile(sourceRoot, stagingRoot, relative); err != nil {
			return err
		}
	}
	return nil
}

func copyVerifiedPackFile(sourceRoot, stagingRoot, relative string) error {
	if err := validateRelativePath(relative, "Runtime Pack artifact"); err != nil {
		return err
	}
	source := filepath.Join(sourceRoot, filepath.FromSlash(relative))
	if err := requireRegularPath(sourceRoot, source); err != nil {
		return err
	}
	before, err := os.Lstat(source)
	if err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return errors.Join(errors.New("Runtime Pack artifact changed during staging"), err)
	}
	destination := filepath.Join(stagingRoot, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	mode := before.Mode().Perm() & 0o755
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	return nil
}

func requireRegularPath(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("Runtime Pack path escapes root: %s", path)
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Runtime Pack symlinks are not allowed: %s", current)
		}
		if current == path && !info.Mode().IsRegular() {
			return fmt.Errorf("Runtime Pack artifact is not a regular file: %s", current)
		}
		if current != path && !info.IsDir() {
			return fmt.Errorf("Runtime Pack parent is not a directory: %s", current)
		}
	}
	return nil
}

func validatePackFileSet(root, manifestName string, artifacts []artifactTarget) error {
	allowed := map[string]struct{}{filepath.ToSlash(filepath.Clean(filepath.FromSlash(manifestName))): {}}
	for _, artifact := range artifacts {
		allowed[filepath.ToSlash(filepath.Clean(filepath.FromSlash(artifact.RelativePath)))] = struct{}{}
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Runtime Pack contains a symlink: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isModelPath(rel) {
			return fmt.Errorf("%w: %s", ErrRuntimePackModelGovernance, rel)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("Runtime Pack contains a special file: %s", rel)
		}
		if _, ok := allowed[rel]; !ok {
			return fmt.Errorf("%w: %s", ErrRuntimePackUnlistedArtifact, rel)
		}
		return nil
	})
}

func rejectModelArtifacts(artifacts []artifactTarget) error {
	for _, artifact := range artifacts {
		if isModelPath(artifact.RelativePath) {
			return fmt.Errorf("%w: %s", ErrRuntimePackModelGovernance, artifact.RelativePath)
		}
	}
	return nil
}

func isModelPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))))
	// Library source trees such as torchvision/models or ultralytics/models
	// are Python code, not weights; only the pack-level models/ dir is.
	if strings.HasPrefix(lower, "models/") {
		return true
	}
	// A .pth directly in site-packages is a site path-config file.
	if filepath.Ext(lower) == ".pth" && filepath.Base(filepath.Dir(lower)) == "site-packages" {
		return false
	}
	switch strings.ToLower(filepath.Ext(lower)) {
	case ".pt", ".pth", ".onnx", ".engine", ".weights":
		return true
	default:
		return false
	}
}

func ensureDisjointRuntimePaths(packRoot, dataDir string) error {
	packAbs, err := filepath.Abs(packRoot)
	if err != nil {
		return err
	}
	packAbs, err = filepath.EvalSymlinks(packAbs)
	if err != nil {
		return err
	}
	dataAbs, err := canonicalFuturePath(dataDir)
	if err != nil {
		return err
	}
	packRel, err := filepath.Rel(packAbs, dataAbs)
	if err != nil {
		return err
	}
	dataRel, err := filepath.Rel(dataAbs, packAbs)
	if err != nil {
		return err
	}
	if packRel == "." || dataRel == "." || packRel != ".." && !strings.HasPrefix(packRel, ".."+string(filepath.Separator)) || dataRel != ".." && !strings.HasPrefix(dataRel, ".."+string(filepath.Separator)) {
		return errors.New("Runtime Pack source and persistent runtime directories must not overlap")
	}
	return nil
}

func canonicalFuturePath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	probe := abs
	var missing []string
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", errors.New("no existing ancestor for persistent Runtime Pack directory")
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
	resolved, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return filepath.Clean(resolved), nil
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("expected a real directory: %s", path)
	}
	return filepath.EvalSymlinks(abs)
}

func failedPackResult(result RuntimePackProvisioningResult, code string, err error) (RuntimePackProvisioningResult, error) {
	result.State, result.ErrorCode = "FAILED", code
	return result, err
}

func packErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrRuntimePackManifestPinMismatch):
		return "MANIFEST_PIN_MISMATCH"
	case errors.Is(err, ErrRuntimePackNotRelease):
		return "RUNTIME_PACK_NOT_RELEASE"
	case errors.Is(err, ErrManifestIncompatible):
		return "RUNTIME_PACK_INCOMPATIBLE"
	case errors.Is(err, ErrManifestInvalidJSON):
		return "MANIFEST_INVALID_JSON"
	case errors.Is(err, ErrArtifactMissing):
		return "ARTIFACT_MISSING"
	case errors.Is(err, ErrArtifactCorrupted):
		return "ARTIFACT_CORRUPTED"
	case errors.Is(err, ErrRuntimePackModelGovernance):
		return "MODEL_GOVERNANCE_BLOCKED"
	case errors.Is(err, ErrRuntimePackUnlistedArtifact):
		return "UNLISTED_RUNTIME_ARTIFACT"
	default:
		return "RUNTIME_PACK_PRECHECK_FAILED"
	}
}
