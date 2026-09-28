import React from 'react';
import { useEnrollment } from '../hooks/useEnrollment';
import { Button } from './Button';

const ERROR_LABELS: Record<string, string> = {
  INVALID_CODE: 'Invalid Code',
  EXPIRED_CODE: 'Expired Code',
  ALREADY_USED: 'Code Already Used',
  RATE_LIMITED: 'Too Many Attempts',
  NETWORK_ERROR: 'Connection Error',
  SERVER_ERROR: 'Server Error',
  PERSISTENCE_ERROR: 'Save Failed',
  EDGE_ID_CONFLICT: 'Device Conflict',
};

export const EnrollmentWizard: React.FC<{ onComplete: () => void }> = ({ onComplete }) => {
  const {
    code,
    deviceName,
    status,
    result,
    errorMessage,
    setCode,
    setDeviceName,
    submit,
    reset,
    canSubmit,
  } = useEnrollment();

  const isClaiming = status === 'CLAIMING';
  const isSuccess = status === 'SUCCESS';
  const isError = !!(errorMessage && status !== 'IDLE' && status !== 'VALIDATING' && status !== 'CLAIMING' && status !== 'SUCCESS');

  if (isSuccess && result) {
    return (
      <section className="card enrollment-card" aria-label="Enrollment Complete">
        <div className="enrollment-success">
          <span className="success-icon" aria-hidden="true">✅</span>
          <h3 className="enrollment-title">Enrollment Complete</h3>
          <p className="enrollment-message">
            This device has been registered with your GEO CAM organization.
          </p>
          <dl className="enrollment-result-grid">
            <dt>Edge ID:</dt>
            <dd><code>{result.edge_id}</code></dd>
            <dt>Device ID:</dt>
            <dd><code>{result.device_id}</code></dd>
            <dt>Status:</dt>
            <dd><span className="badge badge-success">{result.status}</span></dd>
          </dl>
          <Button variant="primary" onClick={onComplete}>
            Continue to Configuration
          </Button>
        </div>
      </section>
    );
  }

  return (
    <section className="card enrollment-card" aria-label="Device Enrollment">
      <h3 className="enrollment-title">Enroll This Device</h3>
      <p className="enrollment-description">
        Enter the one-time enrollment code provided by your GEO CAM administrator.
        The code is in <strong>XXXX-XXXX</strong> format.
      </p>

      <div className="enrollment-form">
        <div className="form-group">
          <label htmlFor="enrollment-code" className="form-label">
            Enrollment Code <span className="required" aria-label="required">*</span>
          </label>
          <input
            id="enrollment-code"
            type="text"
            className={`form-input code-input ${isError ? 'input-error' : ''}`}
            placeholder="XXXX-XXXX"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            disabled={isClaiming}
            maxLength={9}
            autoComplete="off"
            spellCheck={false}
            autoFocus
            aria-describedby={isError ? 'enrollment-error' : undefined}
            aria-invalid={isError}
          />
        </div>

        <div className="form-group">
          <label htmlFor="device-name" className="form-label">
            Device Name <span className="optional">(optional)</span>
          </label>
          <input
            id="device-name"
            type="text"
            className="form-input"
            placeholder="e.g. Office Entrance Edge"
            value={deviceName}
            onChange={(e) => setDeviceName(e.target.value)}
            disabled={isClaiming}
            maxLength={128}
            autoComplete="off"
          />
        </div>

        {isError && (
          <div id="enrollment-error" className="alert alert-danger enrollment-error" role="alert">
            <div className="alert-content">
              <span className="alert-icon" aria-hidden="true">⚠️</span>
              <div>
                <h4 className="alert-title">{ERROR_LABELS[status] || 'Error'}</h4>
                <p className="alert-message">{errorMessage}</p>
              </div>
            </div>
            {status !== 'EDGE_ID_CONFLICT' && (
              <button type="button" className="btn-alert-action" onClick={reset}>
                Try Again
              </button>
            )}
          </div>
        )}

        <div className="enrollment-actions">
          <Button
            variant="primary"
            onClick={submit}
            disabled={!canSubmit}
            loading={isClaiming}
          >
            {isClaiming ? 'Enrolling...' : 'Enroll Device'}
          </Button>
        </div>
      </div>
    </section>
  );
};
