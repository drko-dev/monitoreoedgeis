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
import { ProcessingMode } from './types/installer';
import './App.css';

type ModeWizardStep = 'dashboard' | 'select' | 'review';

export const App: React.FC = () => {
  const { report, state, loading, error, refresh } = useInstaller();
  const [showAdvanced, setShowAdvanced] = useState<boolean>(false);
  const [modeStep, setModeStep] = useState<ModeWizardStep>('dashboard');
  const [selectedMode, setSelectedMode] = useState<ProcessingMode | null>(null);
  const processingMode = useProcessingMode();

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
        <h2 className="loading-text">Inspecting Edge System Environment...</h2>
      </main>
    );
  }

  return (
    <main className="app-container" role="main">
      <Header version={report?.edge_version || 'v1.0.0'} />

      {error && (
        <ErrorAlert
          message={error}
          code="BACKEND_UNAVAILABLE"
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
              {isEnrolled ? 'Configure Processing Mode' : 'Continue'}
            </Button>
            <Button variant="secondary" onClick={refresh}>
              Refresh Diagnostics
            </Button>
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
          {showAdvanced ? 'Hide Advanced Details ▲' : 'Show Advanced Details ▼'}
        </button>
      </div>

      {showAdvanced && report && (
        <section id="advanced-info-panel" className="card advanced-card" aria-label="Advanced Details">
          <h3 className="advanced-title">Advanced Host & Daemon Details</h3>
          <dl className="advanced-grid">
            <dt>Data Directory:</dt>
            <dd><code>{report.data_dir}</code></dd>

            <dt>Privilege Level:</dt>
            <dd><span className="badge badge-neutral">{report.privilege_level}</span></dd>

            <dt>Instance Lock Safe:</dt>
            <dd>
              <strong className="text-success">
                {report.owns_instance_lock ? 'Acquired (Warning)' : 'Safe (Unacquired)'}
              </strong>
            </dd>

            <dt>Edge Background Daemon:</dt>
            <dd>
              <span className={`badge ${report.daemon_running ? 'badge-success' : 'badge-neutral'}`}>
                {report.daemon_running ? 'Active (HTTP 8091)' : 'Stopped / Not Detected'}
              </span>
            </dd>

            <dt>Service Registration:</dt>
            <dd>
              <span className={`badge ${report.service_installed ? 'badge-success' : 'badge-neutral'}`}>
                {report.service_installed ? 'Installed' : 'Not Installed'}
              </span>
            </dd>

            <dt>GPU Accelerator:</dt>
            <dd>{report.gpu_info || 'None / Not Configured'}</dd>
          </dl>
        </section>
      )}
    </main>
  );
};
