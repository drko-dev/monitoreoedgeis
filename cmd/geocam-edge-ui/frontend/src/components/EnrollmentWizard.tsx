import React, { useState } from 'react';
import { useEnrollment } from '../hooks/useEnrollment';
import { Button } from './Button';
import { HelpLink } from './HelpLink';
import { useI18n } from '../i18n/I18nContext';
import { TranslationKey } from '../i18n/translations';
import { docsLinks } from '../config/docsLinks';
import { checkSaaSConnection } from '../services/api';
import { ServerCheckResult } from '../types/installer';

const ERROR_LABEL_KEYS: Record<string, TranslationKey> = {
  INVALID_CODE: 'enrollment.errorInvalidCode',
  EXPIRED_CODE: 'enrollment.errorExpiredCode',
  ALREADY_USED: 'enrollment.errorAlreadyUsed',
  RATE_LIMITED: 'enrollment.errorRateLimited',
  NETWORK_ERROR: 'enrollment.errorNetworkError',
  SERVER_ERROR: 'enrollment.errorServerError',
  PERSISTENCE_ERROR: 'enrollment.errorPersistenceError',
  EDGE_ID_CONFLICT: 'enrollment.errorEdgeIdConflict',
  INVALID_SAAS_URL: 'enrollment.errorInvalidServerUrl',
  SAAS_URL_REQUIRED: 'enrollment.errorInvalidServerUrl',
  CONFIG_WRITE_FAILED: 'enrollment.errorPersistenceError',
};

export const EnrollmentWizard: React.FC<{ onComplete: () => void }> = ({ onComplete }) => {
  const { t } = useI18n();
  const {
    code,
    deviceName,
    saasUrl,
    status,
    result,
    errorMessage,
    setCode,
    setDeviceName,
    setSaasUrl,
    submit,
    reset,
    canSubmit,
  } = useEnrollment();

  const [testingServer, setTestingServer] = useState(false);
  const [testResult, setTestResult] = useState<ServerCheckResult | null>(null);

  const handleTestServer = async () => {
    if (!saasUrl.trim() || testingServer) return;
    setTestingServer(true);
    setTestResult(null);
    try {
      const res = await checkSaaSConnection(saasUrl.trim());
      setTestResult(res);
    } catch {
      setTestResult({ accessible: false, status: 'UNREACHABLE', message: 'Could not connect' });
    } finally {
      setTestingServer(false);
    }
  };

  const isClaiming = status === 'CLAIMING';
  const isSuccess = status === 'SUCCESS';
  const isError = !!(errorMessage && status !== 'IDLE' && status !== 'VALIDATING' && status !== 'CLAIMING' && status !== 'SUCCESS');

  if (isSuccess && result) {
    return (
      <section className="card enrollment-card" aria-label="Enrollment Complete">
        <div className="enrollment-success">
          <span className="success-icon" aria-hidden="true">✅</span>
          <h3 className="enrollment-title">{t('enrollment.completeTitle')}</h3>
          <p className="enrollment-message">{t('enrollment.completeMessage')}</p>
          <dl className="enrollment-result-grid">
            <dt>{t('enrollment.edgeId')}</dt>
            <dd><code>{result.edge_id}</code></dd>
            <dt>{t('enrollment.deviceId')}</dt>
            <dd><code>{result.device_id}</code></dd>
            <dt>{t('enrollment.status')}</dt>
            <dd><span className="badge badge-success">{result.status}</span></dd>
            <dt>{t('enrollment.deviceRole')}</dt>
            <dd>
              <span className="badge badge-info">
                {result.device_kind === 'gateway' ? t('enrollment.deviceRoleGateway') : t('enrollment.deviceRoleEdge')}
              </span>
              {' '}
              <HelpLink href={docsLinks.deviceRole}>{t('enrollment.deviceRoleHelp')}</HelpLink>
            </dd>
          </dl>
          <Button variant="primary" onClick={onComplete}>
            {t('enrollment.continueToConfiguration')}
          </Button>
        </div>
      </section>
    );
  }

  return (
    <section className="card enrollment-card" aria-label="Device Enrollment">
      <h3 className="enrollment-title">{t('enrollment.title')}</h3>
      <p className="enrollment-description">
        {t('enrollment.description')} <strong>XXXX-XXXX</strong>.{' '}
        <HelpLink href={docsLinks.enrollment}>{t('enrollment.whereDoIGetCode')}</HelpLink>
      </p>

      <div className="enrollment-form">
        <div className="form-group">
          <label htmlFor="saas-url" className="form-label">
            {t('enrollment.serverUrlLabel')} <span className="required" aria-label="required">*</span>
          </label>
          <input
            id="saas-url"
            type="url"
            inputMode="url"
            className={`form-input ${status === 'INVALID_SAAS_URL' ? 'input-error' : ''}`}
            placeholder="https://geocam.example.com"
            value={saasUrl}
            onChange={(e) => setSaasUrl(e.target.value)}
            disabled={isClaiming}
            maxLength={2048}
            autoComplete="url"
            spellCheck={false}
            aria-describedby="saas-url-help"
            aria-invalid={status === 'INVALID_SAAS_URL'}
          />
          <small id="saas-url-help" className="form-help">{t('enrollment.serverUrlHelp')}</small>
        </div>

        <div className="form-group">
          <label htmlFor="enrollment-code" className="form-label">
            {t('enrollment.codeLabel')} <span className="required" aria-label="required">*</span>
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
            {t('enrollment.deviceNameLabel')} <span className="optional">{t('enrollment.optional')}</span>
          </label>
          <input
            id="device-name"
            type="text"
            className="form-input"
            placeholder={t('enrollment.deviceNamePlaceholder')}
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
                <h4 className="alert-title">{ERROR_LABEL_KEYS[status] ? t(ERROR_LABEL_KEYS[status]) : t('enrollment.errorGeneric')}</h4>
                <p className="alert-message">{errorMessage}</p>
              </div>
            </div>
            {status !== 'EDGE_ID_CONFLICT' && (
              <button type="button" className="btn-alert-action" onClick={reset}>
                {t('enrollment.tryAgain')}
              </button>
            )}
          </div>
        )}

        <div className="enrollment-actions" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div>
            <Button
              type="button"
              variant="secondary"
              onClick={handleTestServer}
              disabled={!saasUrl.trim() || isClaiming || testingServer}
              loading={testingServer}
            >
              {testingServer ? t('enrollment.testingServer') : t('enrollment.testServer')}
            </Button>
            {testResult && (
              <span
                style={{
                  marginLeft: '0.75rem',
                  fontSize: '0.875rem',
                  color: testResult.accessible ? 'var(--success, #10b981)' : 'var(--danger, #ef4444)',
                }}
              >
                {testResult.accessible
                  ? `✓ ${t('enrollment.serverAccessible')}`
                  : `✗ ${testResult.status === 'INVALID_TLS' ? t('enrollment.serverInvalidTls') : t('enrollment.serverUnreachable')}`}
              </span>
            )}
          </div>
          <Button
            variant="primary"
            onClick={submit}
            disabled={!canSubmit}
            loading={isClaiming}
          >
            {isClaiming ? t('enrollment.enrolling') : t('enrollment.enrollDevice')}
          </Button>
        </div>
      </div>
    </section>
  );
};
