import React from 'react';
import { CameraOnboardingApplyResult, SafeError } from '../types/installer';
import { Button } from './Button';
import { ErrorAlert } from './ErrorAlert';

interface CameraOnboardingResultProps {
  applyResult: CameraOnboardingApplyResult | null;
  applyError: SafeError | null;
  onAddAnother: () => void;
  onDone: () => void;
  onRetry: () => void;
}

export const CameraOnboardingResultView: React.FC<CameraOnboardingResultProps> = ({
  applyResult,
  applyError,
  onAddAnother,
  onDone,
  onRetry,
}) => {
  if (applyError) {
    return (
      <div className="card mode-selector-card">
        <ErrorAlert message={applyError.safe_message} code={applyError.code} onRetry={onRetry} />
        <div className="actions-bar">
          <Button variant="secondary" onClick={onDone}>
            Back
          </Button>
        </div>
      </div>
    );
  }

  if (!applyResult) {
    return null;
  }

  if (applyResult.status === 'SUCCESS') {
    return (
      <div className="card mode-selector-card enrollment-success">
        <span className="success-icon" aria-hidden="true">
          ✓
        </span>
        <h3 className="card-title">Camera added</h3>
        <ul className="check-list">
          <li className="check-item">
            <span className="check-icon pass">✓</span>
            <span className="check-label">Camera configured</span>
          </li>
          <li className="check-item">
            <span className="check-icon pass">✓</span>
            <span className="check-label">Stream validated</span>
          </li>
          <li className="check-item">
            <span className={`check-icon ${applyResult.sync_observed ? 'pass' : ''}`}>
              {applyResult.sync_observed ? '✓' : '…'}
            </span>
            <span className="check-label">GEO CAM Edge recognizes this camera</span>
            <span className="check-value">{applyResult.sync_observed ? 'Synced' : 'Syncing shortly'}</span>
          </li>
        </ul>
        <p className="enrollment-message">{applyResult.safe_message}</p>
        <div className="actions-bar">
          <Button variant="secondary" onClick={onAddAnother}>
            Add another camera
          </Button>
          <Button variant="primary" onClick={onDone}>
            Continue
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="card mode-selector-card">
      <h3 className="card-title">
        {applyResult.status === 'ACTION_REQUIRED' ? 'Action required' : applyResult.status === 'BLOCKED' ? 'Camera not added' : 'Change rolled back'}
      </h3>
      <p className="state-message">{applyResult.safe_message}</p>
      <div className="actions-bar">
        <Button variant="secondary" onClick={onDone}>
          Back
        </Button>
      </div>
    </div>
  );
};
