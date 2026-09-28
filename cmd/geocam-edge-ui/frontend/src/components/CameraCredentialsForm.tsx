import React, { useState } from 'react';
import { OnboardingCandidate, CameraValidationResult, CameraOnboardingPlan } from '../types/installer';
import { candidateDisplayName, candidateChannelLabel } from '../utils/cameraOnboardingDisplay';
import { Button } from './Button';
import { HelpLink } from './HelpLink';
import { useI18n } from '../i18n/I18nContext';
import { docsLinks } from '../config/docsLinks';

interface CameraCredentialsFormProps {
  candidate: OnboardingCandidate;
  validating: boolean;
  validation: CameraValidationResult | null;
  planning: boolean;
  plan: CameraOnboardingPlan | null;
  onValidate: (cameraName: string, manufacturer: string, model: string, username: string, password: string) => void;
  onContinue: () => void;
  onBack: () => void;
}

function checkIcon(status: string): string {
  return status === 'ok' ? '✓' : '✗';
}

export const CameraCredentialsForm: React.FC<CameraCredentialsFormProps> = ({
  candidate,
  validating,
  validation,
  planning,
  plan,
  onValidate,
  onContinue,
  onBack,
}) => {
  const { t } = useI18n();
  const [cameraName, setCameraName] = useState(candidateDisplayName({ ...candidate, model: candidate.model || candidate.manufacturer || 'Camera' }));
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');

  const handleTest = (e: React.FormEvent) => {
    e.preventDefault();
    onValidate(cameraName, candidate.manufacturer || '', candidate.model || '', username, password);
  };

  return (
    <div className="card mode-selector-card">
      <div className="card-header-row">
        <h3 className="card-title">{t('camera.credentialsTitle')}</h3>
        <HelpLink href={docsLinks.cameraCredentials}>{t('camera.credentialsHelp')}</HelpLink>
      </div>
      <p className="state-message">
        {candidate.model || 'Camera'} · {candidate.host}
        {candidateChannelLabel(candidate) && <> · {candidateChannelLabel(candidate)}</>}
      </p>

      <form className="enrollment-form" onSubmit={handleTest}>
        <div className="form-group">
          <label className="form-label" htmlFor="camera-name">
            {t('camera.nameLabel')}
          </label>
          <input
            id="camera-name"
            className="form-input"
            value={cameraName}
            onChange={(e) => setCameraName(e.target.value)}
            required
          />
        </div>
        <div className="form-group">
          <label className="form-label" htmlFor="camera-username">
            {t('camera.usernameLabel')}
          </label>
          <input
            id="camera-username"
            className="form-input"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            required
          />
        </div>
        <div className="form-group">
          <label className="form-label" htmlFor="camera-password">
            {t('camera.passwordLabel')}
          </label>
          <input
            id="camera-password"
            type="password"
            className="form-input"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </div>
        <div className="enrollment-actions">
          <Button type="submit" variant="primary" loading={validating}>
            {t('camera.testConnection')}
          </Button>
        </div>
      </form>

      {validation && (
        <ul className="check-list">
          <li className="check-item">
            <span className={`check-icon ${validation.onvif_status === 'ok' ? 'pass' : 'fail'}`}>
              {checkIcon(validation.onvif_status)}
            </span>
            <span className="check-label">{t('camera.onvifAuth')}</span>
            <span className="check-value">{validation.onvif_status === 'ok' ? t('camera.passed') : validation.onvif_reason}</span>
          </li>
          <li className="check-item">
            <span className={`check-icon ${validation.rtsp_status === 'ok' ? 'pass' : 'fail'}`}>
              {checkIcon(validation.rtsp_status)}
            </span>
            <span className="check-label">{t('camera.rtspConnection')}</span>
            <span className="check-value">{validation.rtsp_status === 'ok' ? t('camera.passed') : validation.rtsp_reason}</span>
          </li>
          {validation.profile && (
            <li className="check-item">
              <span className="check-icon pass">✓</span>
              <span className="check-label">{t('camera.videoProfile')}</span>
              <span className="check-value">
                {validation.profile.codec} · {validation.profile.width}x{validation.profile.height} ·{' '}
                {validation.profile.fps}fps
              </span>
            </li>
          )}
        </ul>
      )}

      {plan && plan.credentials_valid && (
        <>
          <p className="card-header-row">{t('camera.readyToAdd')}</p>
          <dl className="advanced-grid mode-result-grid">
            <dt>{t('camera.camera')}</dt>
            <dd>{plan.camera_name}</dd>
            <dt>{t('camera.stream')}</dt>
            <dd>
              {plan.profile ? `${plan.profile.width}×${plan.profile.height} ${plan.profile.codec} · ${plan.profile.fps} fps` : '—'}
            </dd>
            <dt>{t('camera.processing')}</dt>
            <dd>{plan.processing_mode}</dd>
            <dt>{t('camera.credentials')}</dt>
            <dd className="text-success">{t('camera.verified')}</dd>
          </dl>
        </>
      )}

      <div className="actions-bar">
        <Button variant="secondary" onClick={onBack}>
          {t('camera.back')}
        </Button>
        <Button
          variant="primary"
          onClick={onContinue}
          disabled={!validation?.passed}
          loading={planning}
        >
          {plan?.credentials_valid ? t('camera.addCameraAction') : t('camera.continueAction')}
        </Button>
      </div>
    </div>
  );
};
