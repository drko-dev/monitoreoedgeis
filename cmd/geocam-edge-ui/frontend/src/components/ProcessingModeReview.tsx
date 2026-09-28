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

  return (
    <div className="card mode-selector-card">
      <h3 className="card-title">{t('mode.reviewTitle')}</h3>
      <dl className="advanced-grid mode-result-grid">
        <dt>{t('mode.currentLabel')}</dt>
        <dd>{MODE_LABELS[plan.current_mode] ?? plan.current_mode}</dd>
        <dt>{t('mode.selectedLabel')}</dt>
        <dd>{MODE_LABELS[plan.requested_mode]}</dd>
      </dl>
      {Object.keys(plan.config_changes).length > 0 && (
        <>
          <p className="card-header-row">{t('mode.changes')}</p>
          <ul className="check-list">
            {Object.entries(plan.config_changes).map(([key, value]) => (
              <li key={key} className="check-item">
                <span className="check-label">{describeConfigKey(key)}</span>
                <span className="check-value">{value}</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {plan.components_required && plan.components_required.length > 0 && (
        <>
          <p className="card-header-row">{t('mode.componentsRequired')}</p>
          <ul className="check-list">
            {plan.components_required.map((c) => (
              <li key={c} className="check-item">
                <span className="check-label">{c}</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {plan.warnings && plan.warnings.length > 0 && (
        <p className="mode-card-reason">{plan.warnings.join(' ')}</p>
      )}
      <p className="state-message">{t('mode.restartRequiredLine', { value: plan.restart_required ? t('mode.yes') : t('mode.no') })}</p>
      <div className="actions-bar">
        <Button variant="secondary" onClick={onBack} disabled={applying}>
          {t('mode.back')}
        </Button>
        <Button variant="primary" onClick={onApply} loading={applying}>
          {t('mode.apply')}
        </Button>
      </div>
    </div>
  );
};
