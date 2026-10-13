import React, { useState, useEffect } from 'react';
import { CommissioningStatus, SafeError } from '../types/installer';
import { fetchCommissioningStatus, commissionDaemon } from '../services/api';
import { Button } from './Button';
import { ErrorAlert } from './ErrorAlert';
import { useI18n } from '../i18n/I18nContext';

interface CommissioningViewProps {
  onDone: () => void;
}

export const CommissioningView: React.FC<CommissioningViewProps> = ({ onDone }) => {
  const { t } = useI18n();
  const [status, setStatus] = useState<CommissioningStatus | null>(null);
  const [activating, setActivating] = useState<boolean>(false);
  const [error, setError] = useState<SafeError | null>(null);

  const loadStatus = async () => {
    try {
      const s = await fetchCommissioningStatus();
      setStatus(s);
    } catch (err: unknown) {
      if (err && typeof err === 'object' && 'safe_message' in err) {
        setError(err as SafeError);
      }
    }
  };

  useEffect(() => {
    loadStatus();
    const interval = setInterval(loadStatus, 4000);
    return () => clearInterval(interval);
  }, []);

  const handleActivate = async () => {
    setActivating(true);
    setError(null);
    try {
      const res = await commissionDaemon();
      if (res.status) {
        setStatus(res.status);
      }
      if (res.outcome === 'FAILED') {
        setError({
          code: 'ACTIVATION_FAILED',
          safe_message: res.safe_message,
          recoverable: true,
        });
      }
    } catch (err: unknown) {
      if (err && typeof err === 'object' && 'safe_message' in err) {
        setError(err as SafeError);
      } else {
        setError({
          code: 'ACTIVATION_ERROR',
          safe_message: String(err),
          recoverable: true,
        });
      }
    } finally {
      setActivating(false);
      loadStatus();
    }
  };

  return (
    <div className="card commission-card-container">
      <h3 className="card-title" style={{ textAlign: 'center', marginBottom: '0.5rem' }}>
        {t('commission.title')}
      </h3>
      <p className="product-subtitle" style={{ textAlign: 'center', marginBottom: '1.5rem' }}>
        {t('commission.subtitle')}
      </p>

      {error && (
        <ErrorAlert
          message={error.safe_message}
          code={error.code}
          onRetry={handleActivate}
        />
      )}

      <div className="commission-status-grid">
        <div className="commission-status-item">
          <span className="commission-status-label">{t('commission.service')}</span>
          <span className={`badge ${status?.service_registered ? 'badge-success' : 'badge-neutral'}`}>
            {status?.service_registered ? t('commission.statusInstalled') : t('commission.statusNotInstalled')}
          </span>
        </div>

        <div className="commission-status-item">
          <span className="commission-status-label">{t('commission.daemon')}</span>
          <span className={`badge ${status?.daemon_running ? 'badge-success' : 'badge-neutral'}`}>
            {status?.daemon_running ? t('commission.statusRunning') : t('commission.statusStopped')}
          </span>
        </div>

        <div className="commission-status-item">
          <span className="commission-status-label">{t('commission.health')}</span>
          <span className={`badge ${status?.healthz_ok ? 'badge-success' : 'badge-neutral'}`}>
            {status?.healthz_ok ? t('commission.statusHealthy') : t('commission.statusDegraded')}
          </span>
        </div>

        <div className="commission-status-item">
          <span className="commission-status-label">{t('commission.ready')}</span>
          <span className={`badge ${status?.readyz_ok ? 'badge-success' : 'badge-neutral'}`}>
            {status?.readyz_ok ? t('commission.statusHealthy') : t('commission.statusDegraded')}
          </span>
        </div>

        <div className="commission-status-item">
          <span className="commission-status-label">{t('commission.operational')}</span>
          <span className={`badge ${status?.operational_ok ? 'badge-success' : 'badge-warning'}`}>
            {status?.operational_state || 'UNKNOWN'}
          </span>
        </div>

        <div className="commission-status-item">
          <span className="commission-status-label">{t('commission.saas')}</span>
          <span className={`badge ${status?.saas_sync_ok ? 'badge-success' : 'badge-neutral'}`}>
            {status?.saas_sync_ok ? t('commission.statusSynced') : t('commission.statusNotSynced')}
          </span>
        </div>
      </div>

      <div className="review-block" style={{ textAlign: 'center', padding: '1rem' }}>
        <p style={{ margin: 0, fontWeight: 600, color: status?.overall_ready ? 'var(--success)' : 'var(--text-secondary)' }}>
          {status?.overall_ready ? `✓ ${t('commission.activeConnected')}` : status?.safe_message}
        </p>
      </div>

      <div className="review-actions-centered">
        <Button
          variant="primary"
          onClick={handleActivate}
          loading={activating}
          disabled={activating}
        >
          {activating ? t('commission.activating') : t('commission.actionButton')}
        </Button>
        <Button variant="secondary" onClick={onDone}>
          {t('mode.done')}
        </Button>
      </div>
    </div>
  );
};
