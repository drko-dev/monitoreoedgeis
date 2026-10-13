import { useState, useEffect, useCallback } from 'react';
import { VisionRuntimeStatus } from '../types/installer';
import { fetchVisionRuntimeStatus, prepareVisionRuntime, selectAndImportModel } from '../services/api';

export function useVisionRuntime() {
  const [status, setStatus] = useState<VisionRuntimeStatus | null>(null);
  const [loading, setLoading] = useState<boolean>(false);
  const [provisioning, setProvisioning] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);

  const check = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await fetchVisionRuntimeStatus();
      setStatus(res);
      return res;
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(msg);
      return null;
    } finally {
      setLoading(false);
    }
  }, []);

  const prepare = useCallback(async () => {
    setProvisioning(true);
    setError(null);
    try {
      const res = await prepareVisionRuntime();
      setStatus(res);
      return res;
    } catch (err: unknown) {
      const msg =
        err && typeof err === 'object' && 'safe_message' in err
          ? (err as { safe_message: string }).safe_message
          : err instanceof Error
          ? err.message
          : String(err);
      setError(msg);
      throw err;
    } finally {
      setProvisioning(false);
    }
  }, []);

  const importSingleModel = useCallback(async (modelName: string) => {
    setLoading(true);
    setError(null);
    try {
      const res = await selectAndImportModel(modelName);
      setStatus(res);
      return res;
    } catch (err: unknown) {
      const msg =
        err && typeof err === 'object' && 'safe_message' in err
          ? (err as { safe_message: string }).safe_message
          : err instanceof Error
          ? err.message
          : String(err);
      setError(msg);
      throw err;
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    check();
  }, [check]);

  return {
    status,
    loading,
    provisioning,
    error,
    check,
    prepare,
    importSingleModel,
  };
}
