import {
  CancelFullEdgePreparation,
  GetFullEdgePreparationDiagnostic,
  GetFullEdgePreparationStatus,
  RecoverFullEdgeRuntime,
  StartFullEdgePreparation,
} from '../../wailsjs/go/main/App';
import type {
  FullEdgePreparationDiagnostic,
  FullEdgePreparationSnapshot,
} from '../types/installer';
import { isFullEdgePreparationSnapshot } from '../utils/fullEdgePreparation';

const unavailableSnapshot: FullEdgePreparationSnapshot = {
  state: 'blocked_error',
  progress_percent: 0,
  can_start: false,
  retryable: false,
  cancellation_allowed: false,
  recovery_available: false,
  error_code: 'PREPARATION_NOT_INTEGRATED',
  safe_message: 'Offline Full Edge preparation is not integrated in this build.',
  diagnostics_available: true,
};

export async function fetchFullEdgePreparationStatus(): Promise<FullEdgePreparationSnapshot> {
  try {
    const snapshot = await GetFullEdgePreparationStatus();
    return isFullEdgePreparationSnapshot(snapshot) ? snapshot : { ...unavailableSnapshot };
  } catch {
    // Browser preview and older bindings remain safely unavailable, never ready.
    return { ...unavailableSnapshot };
  }
}

export async function startFullEdgePreparation(requestID: string): Promise<FullEdgePreparationSnapshot> {
  try {
    const snapshot: unknown = await StartFullEdgePreparation(requestID);
    if (!isFullEdgePreparationSnapshot(snapshot)) throw new Error('Invalid preparation status');
    return snapshot;
  } catch (error: unknown) {
    throw toSafePreparationError(error, 'PREPARATION_NOT_INTEGRATED');
  }
}

export async function cancelFullEdgePreparation(operationID: string): Promise<FullEdgePreparationSnapshot> {
  try {
    const snapshot: unknown = await CancelFullEdgePreparation(operationID);
    if (!isFullEdgePreparationSnapshot(snapshot)) throw new Error('Invalid preparation status');
    return snapshot;
  } catch (error: unknown) {
    throw toSafePreparationError(error, 'CANCEL_UNAVAILABLE');
  }
}

export async function recoverFullEdgeRuntime(operationID: string): Promise<FullEdgePreparationSnapshot> {
  try {
    const snapshot: unknown = await RecoverFullEdgeRuntime(operationID);
    if (!isFullEdgePreparationSnapshot(snapshot)) throw new Error('Invalid preparation status');
    return snapshot;
  } catch (error: unknown) {
    throw toSafePreparationError(error, 'RECOVERY_UNAVAILABLE');
  }
}

export async function fetchFullEdgePreparationDiagnostic(operationID: string): Promise<FullEdgePreparationDiagnostic> {
  try {
    const diagnostic: unknown = await GetFullEdgePreparationDiagnostic(operationID);
    if (!diagnostic || typeof diagnostic !== 'object' || typeof (diagnostic as FullEdgePreparationDiagnostic).code !== 'string' || typeof (diagnostic as FullEdgePreparationDiagnostic).summary !== 'string') throw new Error('Invalid diagnostic');
    return diagnostic as FullEdgePreparationDiagnostic;
  } catch {
    return {
      code: 'DIAGNOSTIC_UNAVAILABLE',
      summary: 'A safe diagnostic report is not available in this build.',
    };
  }
}

function toSafePreparationError(error: unknown, fallbackCode: string): { code: string; safe_message: string; recoverable: boolean } {
  let source: unknown = error;
  const message = typeof error === 'string' ? error : error instanceof Error ? error.message : null;
  if (message) {
    try { source = JSON.parse(message) as unknown; } catch { /* Keep the generic safe fallback. */ }
  }
  if (source && typeof source === 'object' && 'code' in source && 'safe_message' in source) {
    const candidate = source as { code: unknown; safe_message: unknown; recoverable?: unknown };
    return {
      code: typeof candidate.code === 'string' ? candidate.code : fallbackCode,
      safe_message: typeof candidate.safe_message === 'string' ? candidate.safe_message : 'Full Edge preparation could not start.',
      recoverable: candidate.recoverable === true,
    };
  }
  return { code: fallbackCode, safe_message: 'Offline Full Edge preparation is not available in this build.', recoverable: false };
}
