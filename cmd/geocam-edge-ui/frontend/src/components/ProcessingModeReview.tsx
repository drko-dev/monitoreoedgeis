import React from 'react';
import { ProcessingModePlan, ProcessingModeApplyResult, SafeError } from '../types/installer';
import { MODE_LABELS, describeConfigKey } from '../utils/processingModeDisplay';
import { Button } from './Button';
import { ErrorAlert } from './ErrorAlert';

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
  if (applyResult) {
    const isSuccess = applyResult.status === 'SUCCESS';
    const isRestartRequired = applyResult.status === 'RESTART_REQUIRED';
    const isRolledBack = applyResult.status === 'ROLLED_BACK';
    return (
      <div className="card mode-selector-card">
        <h3 className="card-title">
          {isSuccess && 'Configuration applied'}
          {isRestartRequired && 'Restart required'}
          {isRolledBack && 'Change rolled back'}
          {applyResult.status === 'BLOCKED' && 'Mode not available'}
        </h3>
        <p className="state-message">{applyResult.safe_message}</p>
        <dl className="advanced-grid mode-result-grid">
          <dt>Requested mode</dt>
          <dd>{MODE_LABELS[applyResult.requested_product_mode]}</dd>
          <dt>Expected profile</dt>
          <dd>{applyResult.expected_effective_profile || '—'}</dd>
          <dt>Actual profile</dt>
          <dd>{applyResult.actual_effective_profile || '—'}</dd>
          <dt>Match</dt>
          <dd className={applyResult.match ? 'text-success' : ''}>{applyResult.match ? 'Yes' : 'No'}</dd>
        </dl>
        <div className="actions-bar">
          <Button variant="secondary" onClick={onDone}>
            Done
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
            Back
          </Button>
        </div>
      </div>
    );
  }

  if (planning || !plan) {
    return (
      <div className="card mode-selector-card">
        <h3 className="card-title">Review configuration</h3>
        <p className="state-message">Checking current configuration…</p>
      </div>
    );
  }

  return (
    <div className="card mode-selector-card">
      <h3 className="card-title">Review configuration</h3>
      <dl className="advanced-grid mode-result-grid">
        <dt>Current</dt>
        <dd>{MODE_LABELS[plan.current_mode] ?? plan.current_mode}</dd>
        <dt>Selected</dt>
        <dd>{MODE_LABELS[plan.requested_mode]}</dd>
      </dl>
      {Object.keys(plan.config_changes).length > 0 && (
        <>
          <p className="card-header-row">Changes</p>
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
          <p className="card-header-row">Components required</p>
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
      <p className="state-message">Restart required: {plan.restart_required ? 'Yes' : 'No'}</p>
      <div className="actions-bar">
        <Button variant="secondary" onClick={onBack} disabled={applying}>
          Back
        </Button>
        <Button variant="primary" onClick={onApply} loading={applying}>
          Apply
        </Button>
      </div>
    </div>
  );
};
