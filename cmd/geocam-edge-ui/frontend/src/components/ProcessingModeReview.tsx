import React from 'react';
import { ProcessingModePlan, ProcessingModeApplyResult, SafeError } from '../types/installer';
import { MODE_LABELS, describeConfigKey } from '../utils/processingModeDisplay';
import { Button } from './Button';
import { ErrorAlert } from './ErrorAlert';
import { useI18n } from '../i18n/I18nContext';

interface ProcessingModeReviewProps {
  plan: ProcessingModePlan | null;
  planning: boolean;
  applying: boolean;
  applyResult: ProcessingModeApplyResult | null;
  applyError: SafeError | null;
  onBack: () => void;
  onApply: () => void;
  onDone: () => void;
}

export const ProcessingModeReview: React.FC<ProcessingModeReviewProps> = ({
  plan,
  planning,
  applying,
  applyResult,
  applyError,
  onBack,
  onApply,
  onDone,
}) => {
  const { t } = useI18n();

  if (applyResult) {
    const isSuccess = applyResult.status === 'SUCCESS';
    const isRestartRequired = applyResult.status === 'RESTART_REQUIRED';
    const isRolledBack = applyResult.status === 'ROLLED_BACK';
    return (
      <div className="card mode-selector-card">
        <h3 className="card-title">
          {isSuccess && t('mode.configurationApplied')}
          {isRestartRequired && t('mode.restartRequired')}
          {isRolledBack && t('mode.changeRolledBack')}
          {applyResult.status === 'BLOCKED' && t('mode.modeNotAvailable')}
        </h3>
        <p className="state-message">{applyResult.safe_message}</p>
        <dl className="advanced-grid mode-result-grid">
          <dt>{t('mode.requestedMode')}</dt>
          <dd>{MODE_LABELS[applyResult.requested_product_mode]}</dd>
          <dt>{t('mode.expectedProfile')}</dt>
          <dd>{applyResult.expected_effective_profile || '—'}</dd>
          <dt>{t('mode.actualProfile')}</dt>
          <dd>{applyResult.actual_effective_profile || '—'}</dd>
          <dt>{t('mode.match')}</dt>
          <dd className={applyResult.match ? 'text-success' : ''}>{applyResult.match ? t('mode.yes') : t('mode.no')}</dd>
        </dl>
        <div className="actions-bar">
          <Button variant="secondary" onClick={onDone}>
            {t('mode.done')}
          </Button>
        </div>
      </div>
    );
  }

  if (applyError) {
    return (
      <div className="card mode-selector-card">
        <ErrorAlert message={applyError.safe_message} code={applyError.code} onRetry={onApply} />
        <div className="actions-bar">
          <Button variant="secondary" onClick={onBack}>
            {t('mode.back')}
          </Button>
        </div>
      </div>
    );
  }

  if (planning || !plan) {
    return (
      <div className="card mode-selector-card">
        <h3 className="card-title">{t('mode.reviewTitle')}</h3>
        <p className="state-message">{t('mode.checking')}</p>
      </div>
    );
  }

  const hasBlockers = Boolean(plan.blockers && plan.blockers.length > 0);

  const translateComponent = (component: string): string => {
    switch (component) {
      case 'Local vision worker process':
        return t('mode.compVisionWorker');
      case 'Person detection model':
        return t('mode.compPersonModel');
      case 'Vehicle detection model':
        return t('mode.compVehicleModel');
      default:
        return component;
    }
  };

  const translateConfigKey = (key: string): string => {
    switch (key) {
      case 'GEOCAM_PROCESSING_MODE':
        return t('mode.configKeyProcessingEngine');
      case 'GEOCAM_VIDEO_PIPELINE_ENABLED':
        return t('mode.configKeyVideoPipeline');
      default:
        return describeConfigKey(key);
    }
  };

  return (
    <div className="card review-card-container">
      <h3 className="card-title" style={{ textAlign: 'center', marginBottom: '1.5rem' }}>
        {t('mode.reviewTitle')}
      </h3>

      {/* Block: Modo actual vs Modo seleccionado */}
      <div className="review-block">
        <div className="review-grid-two-col">
          <div className="review-item">
            <span className="review-item-label">{t('mode.currentLabel')}</span>
            <span className="review-item-value">{MODE_LABELS[plan.current_mode] ?? plan.current_mode}</span>
          </div>
          <div className="review-item">
            <span className="review-item-label">{t('mode.selectedLabel')}</span>
            <span className="review-item-value" style={{ color: 'var(--accent-primary)' }}>
              {MODE_LABELS[plan.requested_mode]}
            </span>
          </div>
        </div>
      </div>

      {hasBlockers && (
        <div
          className="error-alert"
          style={{
            marginBottom: '1.25rem',
            padding: '0.85rem 1rem',
            backgroundColor: 'rgba(239, 68, 68, 0.12)',
            border: '1px solid var(--danger)',
            borderRadius: 'var(--radius-sm)',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
            <span className="check-icon fail" aria-hidden="true">✗</span>
            <strong style={{ color: 'var(--danger)' }}>{t('mode.blockedTitle')}</strong>
          </div>
          <ul style={{ margin: '0.25rem 0 0 1.25rem', padding: 0, color: 'var(--text-primary)' }}>
            {plan.blockers!.map((blocker, idx) => (
              <li key={idx} style={{ marginBottom: '0.25rem' }}>{blocker}</li>
            ))}
          </ul>
        </div>
      )}

      {/* Block: Cambios de configuración */}
      {Object.keys(plan.config_changes).length > 0 && (
        <div className="review-block">
          <h4 className="review-block-title">{t('mode.changes')}</h4>
          <div className="review-grid-two-col">
            {Object.entries(plan.config_changes).map(([key, value]) => (
              <div key={key} className="review-item">
                <span className="review-item-label">{translateConfigKey(key)}</span>
                <span className="review-item-value">{value}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Block: Componentes requeridos */}
      {plan.components_required && plan.components_required.length > 0 && (
        <div className="review-block">
          <h4 className="review-block-title">{t('mode.componentsRequired')}</h4>
          <ul className="component-checklist">
            {plan.components_required.map((c) => (
              <li key={c} className="component-check-item">
                <span className="component-badge-check" aria-hidden="true">✓</span>
                <span>{translateComponent(c)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {plan.warnings && plan.warnings.length > 0 && (
        <p className="mode-card-reason" style={{ marginBottom: '1rem' }}>{plan.warnings.join(' ')}</p>
      )}

      {/* Block: Reinicio requerido */}
      <div className="review-block" style={{ padding: '0.85rem 1.25rem' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span className="review-item-label" style={{ fontSize: '0.85rem' }}>
            {t('mode.restartRequiredLine', { value: '' }).replace(':', '').trim()}
          </span>
          <span className={`badge ${plan.restart_required ? 'badge-warning' : 'badge-neutral'}`}>
            {plan.restart_required ? t('mode.yes') : t('mode.no')}
          </span>
        </div>
      </div>

      {/* Actions: Centered buttons */}
      <div className="review-actions-centered">
        <Button variant="secondary" onClick={onBack} disabled={applying}>
          {t('mode.back')}
        </Button>
        <Button
          variant="primary"
          onClick={onApply}
          loading={applying}
          disabled={hasBlockers || applying}
        >
          {t('mode.apply')}
        </Button>
      </div>
    </div>
  );
};

