import { useState, useEffect, useCallback } from 'react';
import {
  ProcessingModeOption,
  CurrentProcessingMode,
  ProcessingMode,
  ProcessingModePlan,
  ProcessingModeApplyResult,
  SafeError,
} from '../types/installer';
import {
  fetchProcessingModeOptions,
  fetchCurrentProcessingMode,
  planProcessingMode,
  applyProcessingMode,
} from '../services/api';

export function useProcessingMode() {
  const [options, setOptions] = useState<ProcessingModeOption[]>([]);
  const [current, setCurrent] = useState<CurrentProcessingMode | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string | null>(null);

  const [plan, setPlan] = useState<ProcessingModePlan | null>(null);
  const [planning, setPlanning] = useState<boolean>(false);

  const [applying, setApplying] = useState<boolean>(false);
  const [applyResult, setApplyResult] = useState<ProcessingModeApplyResult | null>(null);
  const [applyError, setApplyError] = useState<SafeError | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [opts, cur] = await Promise.all([fetchProcessingModeOptions(), fetchCurrentProcessingMode()]);
      setOptions(opts);
      setCurrent(cur);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Failed to load processing mode information');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const requestPlan = useCallback(async (mode: ProcessingMode) => {
    setPlanning(true);
    setPlan(null);
    try {
      const p = await planProcessingMode({ mode });
      setPlan(p);
      return p;
    } finally {
      setPlanning(false);
    }
  }, []);

  const apply = useCallback(async (mode: ProcessingMode) => {
    setApplying(true);
    setApplyError(null);
    setApplyResult(null);
    try {
      const result = await applyProcessingMode({ mode });
      setApplyResult(result);
      // Re-derive the real current mode from the backend rather than
      // trusting the apply result as a source of truth going forward.
      await load();
      return result;
    } catch (err: unknown) {
      const safeErr =
        err && typeof err === 'object' && 'safe_message' in err
          ? (err as SafeError)
          : { code: 'UNKNOWN_ERROR', safe_message: 'An unexpected error occurred while applying the change.', recoverable: true };
      setApplyError(safeErr);
      throw safeErr;
    } finally {
      setApplying(false);
    }
  }, [load]);

  const resetApply = useCallback(() => {
    setApplyResult(null);
    setApplyError(null);
    setPlan(null);
  }, []);

  return {
    options,
    current,
    loading,
    error,
    refresh: load,
    plan,
    planning,
    requestPlan,
    applying,
    applyResult,
    applyError,
    apply,
    resetApply,
  };
}
