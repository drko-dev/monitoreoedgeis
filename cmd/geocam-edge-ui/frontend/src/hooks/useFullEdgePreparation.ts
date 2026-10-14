import { useCallback, useEffect, useRef, useState } from 'react';
import {
  cancelFullEdgePreparation,
  fetchFullEdgePreparationDiagnostic,
  fetchFullEdgePreparationStatus,
  recoverFullEdgeRuntime,
  startFullEdgePreparation,
} from '../services/fullEdgePreparation';
import type {
  FullEdgePreparationDiagnostic,
  FullEdgePreparationSnapshot,
} from '../types/installer';
import { canCancelPreparation, canRecoverPreparation, canStartPreparation, isPreparationActive } from '../utils/fullEdgePreparation';

let requestSequence = 0;

function newRequestID(): string {
  requestSequence += 1;
  return `offline-prep-${Date.now()}-${requestSequence}`;
}

function errorCode(error: unknown): string {
  return error && typeof error === 'object' && 'code' in error && typeof error.code === 'string'
    ? error.code
    : 'PREPARATION_FAILED';
}

export function useFullEdgePreparation() {
  const [snapshot, setSnapshot] = useState<FullEdgePreparationSnapshot | null>(null);
  const [diagnostic, setDiagnostic] = useState<FullEdgePreparationDiagnostic | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const actionLock = useRef(false);

  const refresh = useCallback(async () => {
    const status = await fetchFullEdgePreparationStatus();
    setSnapshot(status);
    setLoading(false);
    return status;
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (!isPreparationActive(snapshot?.state)) return undefined;
    let timer: number | undefined;
    let disposed = false;
    const poll = async () => {
      await refresh();
      if (!disposed) timer = window.setTimeout(() => { void poll(); }, 900);
    };
    timer = window.setTimeout(() => { void poll(); }, 900);
    return () => {
      disposed = true;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [refresh, snapshot?.state]);

  const runAction = useCallback(async (action: () => Promise<FullEdgePreparationSnapshot>) => {
    if (actionLock.current) return;
    actionLock.current = true;
    setBusy(true);
    setActionError(null);
    try {
      const next = await action();
      setSnapshot(next);
      return next;
    } catch (error: unknown) {
      setActionError(errorCode(error));
      await refresh();
      return null;
    } finally {
      actionLock.current = false;
      setBusy(false);
    }
  }, [refresh]);

  const start = useCallback(() => {
    if (!canStartPreparation(snapshot)) return Promise.resolve(null);
    return runAction(() => startFullEdgePreparation(newRequestID()));
  }, [runAction, snapshot]);

  const cancel = useCallback(() => {
    if (!canCancelPreparation(snapshot) || !snapshot?.operation_id) return Promise.resolve(null);
    return runAction(() => cancelFullEdgePreparation(snapshot.operation_id!));
  }, [runAction, snapshot]);

  const recover = useCallback(() => {
    if (!canRecoverPreparation(snapshot) || !snapshot?.operation_id) return Promise.resolve(null);
    return runAction(() => recoverFullEdgeRuntime(snapshot.operation_id!));
  }, [runAction, snapshot]);

  const loadDiagnostic = useCallback(async () => {
    const value = await fetchFullEdgePreparationDiagnostic(snapshot?.operation_id || '');
    setDiagnostic(value);
    return value;
  }, [snapshot?.operation_id]);

  return { snapshot, diagnostic, loading, busy, actionError, refresh, start, cancel, recover, loadDiagnostic };
}
