import React, { useState } from 'react';
import { useInstaller } from './hooks/useInstaller';
import { useProcessingMode } from './hooks/useProcessingMode';
import { Header } from './components/Header';
import { SystemCard } from './components/SystemCard';
import { StateCard } from './components/StateCard';
import { Button } from './components/Button';
import { ErrorAlert } from './components/ErrorAlert';
import { EnrollmentWizard } from './components/EnrollmentWizard';
import { ProcessingModeSelector } from './components/ProcessingModeSelector';
import { ProcessingModeReview } from './components/ProcessingModeReview';
import { CameraDiscovery } from './components/CameraDiscovery';
import { CameraCredentialsForm } from './components/CameraCredentialsForm';
import { CameraOnboardingResultView } from './components/CameraOnboardingResult';
import { useCameraOnboarding } from './hooks/useCameraOnboarding';
import { candidateKeyForRequest, candidateDisplayName } from './utils/cameraOnboardingDisplay';
import { ProcessingMode, OnboardingCandidate } from './types/installer';
import { useI18n } from './i18n/I18nContext';
import { HelpLink } from './components/HelpLink';
import { docsLinks } from './config/docsLinks';
import './App.css';

type ModeWizardStep = 'dashboard' | 'select' | 'review';
type CameraWizardStep = 'dashboard' | 'discover' | 'credentials' | 'result';

export const App: React.FC = () => {
  const { t } = useI18n();
  const { report, state, loading, error, refresh } = useInstaller();
  const [showAdvanced, setShowAdvanced] = useState<boolean>(false);
  const [modeStep, setModeStep] = useState<ModeWizardStep>('dashboard');
  const [selectedMode, setSelectedMode] = useState<ProcessingMode | null>(null);
  const processingMode = useProcessingMode();

  const [cameraStep, setCameraStep] = useState<CameraWizardStep>('dashboard');
  const [selectedCandidate, setSelectedCandidate] = useState<OnboardingCandidate | null>(null);
  const [pendingCameraForm, setPendingCameraForm] = useState<{
    cameraName: string;
    manufacturer: string;
    model: string;
    username: string;
    password: string;
  } | null>(null);
  const cameraOnboarding = useCameraOnboarding();

  const needsEnrollment = state?.state === 'NEEDS_ENROLLMENT' || state?.state === 'NEW';
  const isEnrolled = state?.state === 'ENROLLED';

  const handleContinue = () => {
    if (isEnrolled) {
      setSelectedMode(processingMode.current?.mode ?? null);
      setModeStep('select');
      return;
    }
    refresh();
  };

  const handleAddCamera = () => {
    setCameraStep('discover');
    cameraOnboarding.scan();
  };

  const handleSelectCandidate = (candidate: OnboardingCandidate) => {
    setSelectedCandidate(candidate);
    setPendingCameraForm(null);
    cameraOnboarding.reset();
    setCameraStep('credentials');
  };

  const handleValidateCamera = async (
    cameraName: string,
    manufacturer: string,
    model: string,
    username: string,
    password: string,
  ) => {
    if (!selectedCandidate) return;
    setPendingCameraForm({ cameraName, manufacturer, model, username, password });
    const candidateKey = candidateKeyForRequest(selectedCandidate);
    const validation = await cameraOnboarding.validate(candidateKey, username, password);
    if (validation.passed) {
      await cameraOnboarding.requestPlan(candidateKey, cameraName, manufacturer, model, username, password);
    }
  };

  const handleCameraContinue = async () => {
    if (!selectedCandidate || !pendingCameraForm) return;
    setCameraStep('result');
    try {
      await cameraOnboarding.apply(
        candidateKeyForRequest(selectedCandidate),
        pendingCameraForm.cameraName,
        pendingCameraForm.manufacturer,
        pendingCameraForm.model,
        pendingCameraForm.username,
        pendingCameraForm.password,
      );
    } catch {
      // surfaced via cameraOnboarding.applyError in the result screen
    }
  };

  const handleCameraDone = () => {
    cameraOnboarding.reset();
    setSelectedCandidate(null);
    setPendingCameraForm(null);
    setCameraStep('dashboard');
  };

  const handleAddAnotherCamera = () => {
    cameraOnboarding.reset();
    setSelectedCandidate(null);
    setPendingCameraForm(null);
    setCameraStep('discover');
    cameraOnboarding.scan();
  };

  const handleSelectContinue = async () => {
    if (!selectedMode) return;
    setModeStep('review');
    await processingMode.requestPlan(selectedMode);
  };

  const handleApply = async () => {
    if (!selectedMode) return;
    try {
      await processingMode.apply(selectedMode);
    } catch {
      // surfaced via processingMode.applyError in the review screen
    }
  };

  const handleModeDone = () => {
    processingMode.resetApply();
    setModeStep('dashboard');
  };

  if (loading) {
    return (
      <main className="app-container loading-container" role="main">
        <div className="spinner-large" aria-hidden="true" />
        <h2 className="loading-text">{t('app.loading')}</h2>
      </main>
    );
  }

  return (
    <main className="app-container" role="main">
      <Header version={report?.edge_version || 'v1.0.0'} />

      {error && (
        <ErrorAlert
          message={error}
          code={t('app.errorCode')}
          onRetry={refresh}
        />
      )}

      {needsEnrollment ? (
        <EnrollmentWizard onComplete={refresh} />
      ) : isEnrolled && modeStep === 'select' ? (
        <ProcessingModeSelector
          options={processingMode.options}
          currentMode={processingMode.current?.mode ?? null}
          selected={selectedMode}
          onSelect={setSelectedMode}
          onContinue={handleSelectContinue}
          loading={processingMode.planning}
        />
      ) : isEnrolled && modeStep === 'review' ? (
        <ProcessingModeReview
          plan={processingMode.plan}
          planning={processingMode.planning}
          applying={processingMode.applying}
          applyResult={processingMode.applyResult}
          applyError={processingMode.applyError}
          onBack={() => setModeStep('select')}
          onApply={handleApply}
          onDone={handleModeDone}
        />
      ) : isEnrolled && cameraStep === 'discover' ? (
        <CameraDiscovery
          scanState={cameraOnboarding.scanState}
          candidates={cameraOnboarding.candidates}
          scanError={cameraOnboarding.scanError}
          onScan={cameraOnboarding.scan}
          onSelect={handleSelectCandidate}
        />
      ) : isEnrolled && cameraStep === 'credentials' && selectedCandidate ? (
        <CameraCredentialsForm
          candidate={selectedCandidate}
          validating={cameraOnboarding.validating}
          validation={cameraOnboarding.validation}
          planning={cameraOnboarding.planning}
          plan={cameraOnboarding.plan}
          onValidate={handleValidateCamera}
          onContinue={handleCameraContinue}
          onBack={() => setCameraStep('discover')}
        />
      ) : isEnrolled && cameraStep === 'result' ? (
        <CameraOnboardingResultView
          applyResult={cameraOnboarding.applyResult}
          applyError={cameraOnboarding.applyError}
          cameraLabel={selectedCandidate ? candidateDisplayName(selectedCandidate) : undefined}
          onAddAnother={handleAddAnotherCamera}
          onDone={handleCameraDone}
          onRetry={handleCameraContinue}
        />
      ) : (
        <>
          <div className="dashboard-grid">
            {report && <SystemCard report={report} />}
            {state && <StateCard state={state} />}
          </div>

          <div className="actions-bar">
            <Button
              variant="primary"
              onClick={handleContinue}
              disabled={state?.state === 'BLOCKED'}
            >
              {isEnrolled ? t('app.configureProcessingMode') : t('app.continue')}
            </Button>
            {isEnrolled && (
              <Button variant="secondary" onClick={handleAddCamera}>
                {t('app.addCamera')}
              </Button>
            )}
            <Button variant="secondary" onClick={refresh}>
              {t('app.refreshDiagnostics')}
            </Button>
            <HelpLink href={docsLinks.commissioning}>{t('help.commissioningHelp')}</HelpLink>
          </div>
        </>
      )}

      <div className="actions-bar">
        <button
          type="button"
          className="btn-link"
          onClick={() => setShowAdvanced(!showAdvanced)}
          aria-expanded={showAdvanced}
          aria-controls="advanced-info-panel"
        >
          {showAdvanced ? t('app.hideAdvanced') : t('app.showAdvanced')}
        </button>
      </div>

      {showAdvanced && report && (
        <section id="advanced-info-panel" className="card advanced-card" aria-label="Advanced Details">
          <h3 className="advanced-title">{t('app.advancedTitle')}</h3>
          <dl className="advanced-grid">
            <dt>{t('app.dataDirectory')}</dt>
            <dd><code>{report.data_dir}</code></dd>

            <dt>{t('app.privilegeLevel')}</dt>
            <dd><span className="badge badge-neutral">{report.privilege_level}</span></dd>

            <dt>{t('app.instanceLockSafe')}</dt>
            <dd>
              <strong className="text-success">
                {report.owns_instance_lock ? t('app.instanceLockAcquired') : t('app.instanceLockSafeValue')}
              </strong>
            </dd>

            <dt>{t('app.daemon')}</dt>
            <dd>
              <span className={`badge ${report.daemon_running ? 'badge-success' : 'badge-neutral'}`}>
                {report.daemon_running ? t('app.daemonActive') : t('app.daemonStopped')}
              </span>
            </dd>

            <dt>{t('app.serviceRegistration')}</dt>
            <dd>
              <span className={`badge ${report.service_installed ? 'badge-success' : 'badge-neutral'}`}>
                {report.service_installed ? t('app.serviceInstalled') : t('app.serviceNotInstalled')}
              </span>
            </dd>

            <dt>{t('app.gpuAccelerator')}</dt>
            <dd>{report.gpu_info || t('app.gpuNone')}</dd>
          </dl>
        </section>
      )}
    </main>
  );
};
