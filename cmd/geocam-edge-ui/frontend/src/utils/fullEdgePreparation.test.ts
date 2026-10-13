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
  state: 'blocked_error',
  progress_percent: 0,
  can_start: false,
  retryable: false,
  cancellation_allowed: false,
  recovery_available: false,
  error_code: 'PREPARATION_NOT_INTEGRATED',
  diagnostics_available: true,
};

test('runtime_ready is not shown without complete backend certification', () => {
  assert.equal(isCertifiedReady({ ...blocked, state: 'runtime_ready', progress_percent: 100 }), false);
  assert.equal(isCertifiedReady({ ...blocked, state: 'installing_dependencies', progress_percent: 100 }), false);
});

test('only explicit pack detection or safe retry can start, and active work cannot duplicate', () => {
  assert.equal(canStartPreparation(blocked), false);
  assert.equal(canStartPreparation({ ...blocked, state: 'runtime_pack_detected', can_start: true }), true);
  assert.equal(canStartPreparation({ ...blocked, state: 'recoverable_error', can_start: true, retryable: true }), true);
  assert.equal(canStartPreparation({ ...blocked, state: 'verifying_integrity', can_start: true }), false);
});

test('cancel and recovery are limited to explicit backend-safe states', () => {
  assert.equal(canCancelPreparation({ ...blocked, state: 'preparing_worker', cancellation_allowed: true }), true);
  assert.equal(canCancelPreparation({ ...blocked, state: 'preparing_worker', cancellation_allowed: false }), false);
  assert.equal(canRecoverPreparation({ ...blocked, state: 'recovery_pending', recovery_available: true }), true);
  assert.equal(canRecoverPreparation({ ...blocked, state: 'recovering', recovery_available: true }), false);
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
  assert.equal(isFullEdgePreparationSnapshot({ ...blocked, state: 'runtime_ready' }), true);
  assert.equal(isFullEdgePreparationSnapshot({ ...blocked, progress_percent: 101 }), false);
  assert.equal(isFullEdgePreparationSnapshot({ ...blocked, can_start: 'yes' }), false);
  assert.equal(isFullEdgePreparationSnapshot(null), false);
});

test('malformed certification fields fail closed without throwing', () => {
  assert.equal(isCertifiedReady({ ...blocked, state: 'runtime_ready', progress_percent: 100, certification: { certification_id: 3 } as never }), false);
  assert.equal(safeDiagnosticText('operation failed\n at /Users/operator/file.js', 'unavailable'), 'unavailable');
});
