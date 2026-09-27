import React, { useState } from 'react';
import { useInstaller } from './hooks/useInstaller';
import { Header } from './components/Header';
import { SystemCard } from './components/SystemCard';
import { StateCard } from './components/StateCard';
import { Button } from './components/Button';
import { ErrorAlert } from './components/ErrorAlert';
import './App.css';

export const App: React.FC = () => {
  const { report, state, loading, error, refresh } = useInstaller();
  const [showAdvanced, setShowAdvanced] = useState<boolean>(false);
  const [continueNotice, setContinueNotice] = useState<string | null>(null);

  const handleContinue = () => {
    if (!state) return;

    if (state.state === 'NEEDS_ENROLLMENT' || state.state === 'NEW') {
      setContinueNotice(
        'SaaS Enrollment wizard will be available in milestone UX-2. No productive actions taken in UX-1 shell.'
      );
    } else if (state.state === 'ENROLLED') {
      setContinueNotice(
        'Device is already enrolled. Configuration wizard will be available in milestone UX-3.'
      );
    } else if (state.state === 'BLOCKED') {
      setContinueNotice(
        'Installation is blocked due to platform incompatibility or missing prerequisites.'
      );
    }
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

      {continueNotice && (
        <div className="alert alert-info" role="status">
          <div className="alert-content">
            <span className="alert-icon" aria-hidden="true">ℹ️</span>
            <div>
              <h4 className="alert-title">Milestone Boundary</h4>
              <p className="alert-message">{continueNotice}</p>
            </div>
          </div>
          <button
            type="button"
            className="btn-alert-close"
            onClick={() => setContinueNotice(null)}
            aria-label="Close notification"
          >
            ✕
          </button>
        </div>
      )}

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
          Continue
        </Button>
        <Button variant="secondary" onClick={refresh}>
          Refresh Diagnostics
        </Button>
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
