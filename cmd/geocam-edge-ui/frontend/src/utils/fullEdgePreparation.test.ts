import assert from 'node:assert/strict';
import test from 'node:test';
import type { FullEdgePreparationSnapshot } from '../types/installer.ts';
import {
  canCancelPreparation,
  canRecoverPreparation,
  canStartPreparation,
  isCertifiedReady,
  isFullEdgePreparationSnapshot,
  isProgressValid,
  preparationErrorKey,
  safeDiagnosticText,
} from './fullEdgePreparation.ts';

const blocked: FullEdgePreparationSnapshot = {
  state: 'FAILED',
  progress_percent: 0,
  can_start: false,
  retryable: false,
  cancellation_allowed: false,
  recovery_available: false,
  error_code: 'PREPARATION_NOT_INTEGRATED',
  diagnostics_available: true,
};

test('READY is not shown without complete backend certification', () => {
  assert.equal(isCertifiedReady({ ...blocked, state: 'READY', progress_percent: 100 }), false);
  assert.equal(isCertifiedReady({ ...blocked, state: 'PREPARING', progress_percent: 100 }), false);
});

test('only explicit NOT_PREPARED, CANCELLED, or safe retry can start, and active work cannot duplicate', () => {
  assert.equal(canStartPreparation(blocked), false);
  assert.equal(canStartPreparation({ ...blocked, state: 'NOT_PREPARED', can_start: true }), true);
  assert.equal(canStartPreparation({ ...blocked, state: 'FAILED', can_start: true, retryable: true }), true);
  assert.equal(canStartPreparation({ ...blocked, state: 'CANCELLED', can_start: true }), true);
  assert.equal(canStartPreparation({ ...blocked, state: 'PREPARING', can_start: true }), false);
  assert.equal(canStartPreparation({ ...blocked, state: 'VERIFYING', can_start: true }), false);
});

test('cancel and recovery are limited to explicit backend-safe states', () => {
  assert.equal(canCancelPreparation({ ...blocked, state: 'PREPARING', cancellation_allowed: true }), true);
  assert.equal(canCancelPreparation({ ...blocked, state: 'PREPARING', cancellation_allowed: false }), false);
  assert.equal(canCancelPreparation({ ...blocked, state: 'FAILED', cancellation_allowed: true }), false);
  assert.equal(canRecoverPreparation({ ...blocked, state: 'RECOVERY_REQUIRED', recovery_available: true }), true);
  assert.equal(canRecoverPreparation({ ...blocked, state: 'PREPARING', recovery_available: true }), false);
});

test('progress and error mapping remain bounded and user-safe', () => {
  assert.equal(isProgressValid({ ...blocked, progress_percent: 50 }), true);
  assert.equal(isProgressValid({ ...blocked, progress_percent: 101 }), false);
  assert.equal(preparationErrorKey('HASH_MISMATCH'), 'preparation.errorHashMismatch');
  assert.equal(preparationErrorKey('SENSITIVE_STACK_TRACE'), 'preparation.errorGeneric');
});

test('technical diagnostics redact host paths, secret values, and stack traces', () => {
  assert.equal(safeDiagnosticText('missing file /Users/operator/models/model.pt', 'unavailable'), 'missing file [PATH]');
  assert.equal(safeDiagnosticText('password=supersecret', 'unavailable'), '[REDACTED]');
  assert.equal(safeDiagnosticText('  at worker (/private/tmp/worker.js:1:1)', 'unavailable'), 'unavailable');
  assert.equal(safeDiagnosticText(undefined, 'unavailable'), 'unavailable');
});

test('malformed backend status is rejected rather than reaching the UI', () => {
  assert.equal(isFullEdgePreparationSnapshot({ ...blocked, state: 'READY' }), true);
  assert.equal(isFullEdgePreparationSnapshot({ ...blocked, state: 'CHECKING' }), true);
  assert.equal(isFullEdgePreparationSnapshot({ ...blocked, progress_percent: 101 }), false);
  assert.equal(isFullEdgePreparationSnapshot({ ...blocked, can_start: 'yes' }), false);
  assert.equal(isFullEdgePreparationSnapshot(null), false);
});

test('malformed certification fields fail closed without throwing', () => {
  assert.equal(isCertifiedReady({ ...blocked, state: 'READY', progress_percent: 100, certification: { certification_id: 3 } as never }), false);
  assert.equal(safeDiagnosticText('operation failed\n at /Users/operator/file.js', 'unavailable'), 'unavailable');
});

test('all components must report READY for readiness certification', () => {
  const validCert = {
    certification_id: 'cert-1',
    runtime_pack_version: 'pack-1',
    runtime_manifest_sha256: 'a'.repeat(64),
    platform: 'darwin/arm64',
    evidence_level: 'verified',
    runtime_pack_verified: true,
    dependencies_verified: true,
    vision_worker_ready: true,
    ffmpeg_verified: true,
    ffprobe_verified: true,
    model_pack_version: 'models-1',
    model_manifest_sha256: 'b'.repeat(64),
    model_authorized: true,
    model_integrity_verified: true,
    inference_smoke_test_passed: true,
    certified_at: new Date().toISOString(),
  };

  const readyWithComponents: FullEdgePreparationSnapshot = {
    ...blocked,
    state: 'READY',
    progress_percent: 100,
    certification: validCert,
    components: [
      { key: 'python', name: 'Python', status: 'READY' },
      { key: 'wheelhouse', name: 'Wheelhouse', status: 'READY' },
      { key: 'worker', name: 'Vision Worker', status: 'READY' },
      { key: 'ffmpeg', name: 'FFmpeg', status: 'READY' },
      { key: 'ffprobe', name: 'FFprobe', status: 'READY' },
      { key: 'authorized_models', name: 'Modelos autorizados', status: 'PENDING' },
    ],
  };

  // One component is PENDING, so not ready
  assert.equal(isCertifiedReady(readyWithComponents), false);

  // When all components are READY
  readyWithComponents.components![5].status = 'READY';
  assert.equal(isCertifiedReady(readyWithComponents), true);
});
