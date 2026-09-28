import { useState, useEffect, useCallback } from 'react';
import { SystemReport, InstallerState } from '../types/installer';
import { fetchSystemReport, fetchInstallerState } from '../services/api';
import { useI18n } from '../i18n/I18nContext';

export function useInstaller() {
  const { t } = useI18n();
  const [report, setReport] = useState<SystemReport | null>(null);
  const [state, setState] = useState<InstallerState | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string | null>(null);

  const loadData = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [sysReport, instState] = await Promise.all([
        fetchSystemReport(),
        fetchInstallerState(),
      ]);
      setReport(sysReport);
      setState(instState);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : t('errors.backendUnavailable');
      setError(message);
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  return {
    report,
    state,
    loading,
    error,
    refresh: loadData,
  };
}
