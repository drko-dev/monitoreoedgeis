import { useState, useEffect, useCallback } from 'react';
import { DependencyStatus } from '../types/installer';
import { fetchFFmpegStatus, installFFmpeg } from '../services/api';

export function useFFmpeg() {
  const [status, setStatus] = useState<DependencyStatus | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [installing, setInstalling] = useState<boolean>(false);
  const [verifying, setVerifying] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await fetchFFmpegStatus();
      setStatus(res);
      return res;
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
      return null;
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const install = useCallback(async () => {
    setInstalling(true);
    setError(null);
    try {
      await installFFmpeg();
      setInstalling(false);
      setVerifying(true);
      // Verify final status
      const updated = await fetchFFmpegStatus();
      setStatus(updated);
      return updated;
    } catch (err: unknown) {
      const msg =
        err && typeof err === 'object' && 'safe_message' in err
          ? (err as { safe_message: string }).safe_message
          : err instanceof Error
            ? err.message
            : String(err);
      setError(msg);
      // Refresh status in case partial changes occurred
      await refresh();
      return null;
    } finally {
      setInstalling(false);
      setVerifying(false);
    }
  }, [refresh]);

  return {
    status,
    loading,
    installing,
    verifying,
    error,
    refresh,
    install,
  };
}
