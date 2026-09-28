import React from 'react';
import { CameraOnboardingApplyResult, SafeError } from '../types/installer';
import { Button } from './Button';
import { ErrorAlert } from './ErrorAlert';
import { useI18n } from '../i18n/I18nContext';

interface CameraOnboardingResultProps {
  applyResult: CameraOnboardingApplyResult | null;
  applyError: SafeError | null;
  // Device + channel description for the camera this result is about (e.g.
  // "NVR-8CH · Channel 2 of 4 (CH2)"). Sourced from the candidate the
  // operator selected, never re-derived from applyResult.candidate_key, so a
  // DVR/NVR's channels can never be confused with each other here.
  cameraLabel?: string;
  onAddAnother: () => void;
  onDone: () => void;
  onRetry: () => void;
}

export const CameraOnboardingResultView: React.FC<CameraOnboardingResultProps> = ({
  applyResult,
  applyError,
  cameraLabel,
  onAddAnother,
  onDone,
  onRetry,
}) => {
  const { t } = useI18n();

  if (applyError) {
    return (
      <div className="card mode-selector-card">
        <ErrorAlert message={applyError.safe_message} code={applyError.code} onRetry={onRetry} />
        <div className="actions-bar">
          <Button variant="secondary" onClick={onDone}>
            {t('camera.back')}
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
        <h3 className="card-title">{t('camera.added')}</h3>
        {cameraLabel && <p className="state-message">{cameraLabel}</p>}
        <ul className="check-list">
          <li className="check-item">
            <span className="check-icon pass">✓</span>
            <span className="check-label">{t('camera.configured')}</span>
          </li>
          <li className="check-item">
            <span className="check-icon pass">✓</span>
            <span className="check-label">{t('camera.streamValidated')}</span>
          </li>
          <li className="check-item">
            <span className={`check-icon ${applyResult.sync_observed ? 'pass' : ''}`}>
              {applyResult.sync_observed ? '✓' : '…'}
            </span>
            <span className="check-label">{t('camera.recognizes')}</span>
            <span className="check-value">{applyResult.sync_observed ? t('camera.synced') : t('camera.syncingShortly')}</span>
          </li>
        </ul>
        <p className="enrollment-message">{applyResult.safe_message}</p>
        <div className="actions-bar">
          <Button variant="secondary" onClick={onAddAnother}>
            {t('camera.addAnother')}
          </Button>
          <Button variant="primary" onClick={onDone}>
            {t('camera.continue')}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="card mode-selector-card">
      <h3 className="card-title">
        {applyResult.status === 'ACTION_REQUIRED' ? t('camera.actionRequired') : applyResult.status === 'BLOCKED' ? t('camera.notAdded') : t('camera.rolledBack')}
      </h3>
      <p className="state-message">{applyResult.safe_message}</p>
      <div className="actions-bar">
        <Button variant="secondary" onClick={onDone}>
          {t('camera.back')}
        </Button>
      </div>
    </div>
  );
};
