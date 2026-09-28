import React, { useState } from 'react';
import { OnboardingCandidate, CameraValidationResult, CameraOnboardingPlan } from '../types/installer';
import { Button } from './Button';

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

const CHECK_LABELS: Record<string, string> = {
  ok: 'Passed',
};

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
  const [cameraName, setCameraName] = useState(candidate.model || candidate.manufacturer || 'Camera');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');

  const handleTest = (e: React.FormEvent) => {
    e.preventDefault();
    onValidate(cameraName, candidate.manufacturer || '', candidate.model || '', username, password);
  };

  return (
    <div className="card mode-selector-card">
      <h3 className="card-title">Camera credentials</h3>
      <p className="state-message">
        {candidate.model || 'Camera'} · {candidate.host}
      </p>

      <form className="enrollment-form" onSubmit={handleTest}>
        <div className="form-group">
          <label className="form-label" htmlFor="camera-name">
            Camera name
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
            Username
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
            Password
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
            Test connection
          </Button>
        </div>
      </form>

      {validation && (
        <ul className="check-list">
          <li className="check-item">
            <span className={`check-icon ${validation.onvif_status === 'ok' ? 'pass' : 'fail'}`}>
              {checkIcon(validation.onvif_status)}
            </span>
            <span className="check-label">ONVIF authentication</span>
            <span className="check-value">{validation.onvif_status === 'ok' ? CHECK_LABELS.ok : validation.onvif_reason}</span>
          </li>
          <li className="check-item">
            <span className={`check-icon ${validation.rtsp_status === 'ok' ? 'pass' : 'fail'}`}>
              {checkIcon(validation.rtsp_status)}
            </span>
            <span className="check-label">RTSP connection</span>
            <span className="check-value">{validation.rtsp_status === 'ok' ? CHECK_LABELS.ok : validation.rtsp_reason}</span>
          </li>
          {validation.profile && (
            <li className="check-item">
              <span className="check-icon pass">✓</span>
              <span className="check-label">Video profile</span>
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
          <p className="card-header-row">Camera ready to add</p>
          <dl className="advanced-grid mode-result-grid">
            <dt>Camera</dt>
            <dd>{plan.camera_name}</dd>
            <dt>Stream</dt>
            <dd>
              {plan.profile ? `${plan.profile.width}×${plan.profile.height} ${plan.profile.codec} · ${plan.profile.fps} fps` : '—'}
            </dd>
            <dt>Processing</dt>
            <dd>{plan.processing_mode}</dd>
            <dt>Credentials</dt>
            <dd className="text-success">Verified</dd>
          </dl>
        </>
      )}

      <div className="actions-bar">
        <Button variant="secondary" onClick={onBack}>
          Back
        </Button>
        <Button
          variant="primary"
          onClick={onContinue}
          disabled={!validation?.passed}
          loading={planning}
        >
          {plan?.credentials_valid ? 'Add camera' : 'Continue'}
        </Button>
      </div>
    </div>
  );
};
