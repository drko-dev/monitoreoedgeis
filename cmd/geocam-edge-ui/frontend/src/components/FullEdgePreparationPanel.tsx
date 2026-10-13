import React, { useState } from 'react';
import { useI18n } from '../i18n/I18nContext';
import type { FullEdgePreparationSnapshot, FullEdgePreparationState } from '../types/installer';
import { TranslationKey } from '../i18n/translations';
import { isCertifiedReady, preparationErrorKey, preparationStages, safeDiagnosticText } from '../utils/fullEdgePreparation';
import { useFullEdgePreparation } from '../hooks/useFullEdgePreparation';
import { Button } from './Button';

interface FullEdgePreparationPanelProps {
  selected: boolean;
}

function stateLabelKey(snapshot: FullEdgePreparationSnapshot | null, ready: boolean): TranslationKey {
  if (!snapshot) return 'preparation.state.loading';
  if (snapshot.state === 'runtime_ready' && !ready) return 'preparation.state.invalidCertification';
  return `preparation.state.${snapshot.state}` as TranslationKey;
}

function stageLabelKey(state: FullEdgePreparationState): TranslationKey {
  return `preparation.state.${state}` as TranslationKey;
}

export const FullEdgePreparationPanel: React.FC<FullEdgePreparationPanelProps> = ({ selected }) => {
  const { t } = useI18n();
  const { snapshot, diagnostic, loading, busy, actionError, start, cancel, recover, loadDiagnostic } = useFullEdgePreparation();
  const [diagnosticOpen, setDiagnosticOpen] = useState(false);
  if (!selected) return null;

  const ready = isCertifiedReady(snapshot);
  const errorCode = actionError || snapshot?.error_code;
  const errorKey = errorCode ? preparationErrorKey(errorCode) : null;
  const stageIndex = snapshot ? preparationStages.indexOf(snapshot.state as typeof preparationStages[number]) : -1;
  const progress = snapshot && Number.isInteger(snapshot.progress_percent)
    ? Math.max(0, Math.min(100, snapshot.progress_percent))
    : 0;

  const toggleDiagnostic = async () => {
    const next = !diagnosticOpen;
    setDiagnosticOpen(next);
    if (next) await loadDiagnostic();
  };

  return (
    <section className="card full-edge-preparation" aria-labelledby="full-edge-preparation-title">
      <div className="card-header-row">
        <h3 className="card-title" id="full-edge-preparation-title">{t('preparation.title')}</h3>
        <span className={`badge ${ready ? 'badge-success' : 'badge-warning'}`}>
          {t(stateLabelKey(snapshot, ready))}
        </span>
      </div>

      <p className="mode-card-description">{t('preparation.description')}</p>

      {loading && <p role="status">{t('preparation.checking')}</p>}

      {snapshot && !ready && errorKey && (
        <div className="full-edge-preparation-error" role="alert">
          <strong>{t(errorKey)}</strong>
          {snapshot.safe_message && snapshot.error_code === 'PREPARATION_NOT_INTEGRATED' && (
            <p>{t('preparation.notIntegratedDetail')}</p>
          )}
        </div>
      )}

      {snapshot && ready && (
        <div className="full-edge-preparation-success" role="status">
          <strong>{t('preparation.certified')}</strong>
          <span>{snapshot.certification?.runtime_pack_version} · {snapshot.certification?.platform}</span>
        </div>
      )}

      {snapshot && snapshot.state !== 'initial' && snapshot.state !== 'blocked_error' && (
        <div className="full-edge-preparation-progress">
          <div className="full-edge-preparation-progress-label">
            <span>{t('preparation.currentStage', { stage: t(stageLabelKey(snapshot.state)) })}</span>
            <span>{progress}%</span>
          </div>
          <progress max={100} value={ready ? 100 : progress} aria-label={t('preparation.progressLabel')} />
          <ol aria-label={t('preparation.stages')}>
            {preparationStages.map((stage, index) => (
              <li key={stage} aria-current={!ready && index === stageIndex ? 'step' : undefined}>
                {t(stageLabelKey(stage))}
              </li>
            ))}
          </ol>
        </div>
      )}

      <div className="full-edge-preparation-actions">
        <Button
          variant="primary"
          onClick={() => void start()}
          loading={busy && !snapshot?.cancellation_allowed}
          disabled={loading || busy || !snapshot?.can_start}
        >
          {snapshot?.state === 'recoverable_error' ? t('preparation.retry') : t('preparation.prepare')}
        </Button>
        {snapshot?.cancellation_allowed && (
          <Button variant="secondary" onClick={() => void cancel()} disabled={busy}>
            {t('preparation.cancel')}
          </Button>
        )}
        {snapshot?.recovery_available && (
          <Button variant="secondary" onClick={() => void recover()} loading={busy} disabled={busy}>
            {t('preparation.recover')}
          </Button>
        )}
        {snapshot?.diagnostics_available && (
          <Button variant="outline" onClick={() => void toggleDiagnostic()}>
            {diagnosticOpen ? t('preparation.hideDiagnostic') : t('preparation.showDiagnostic')}
          </Button>
        )}
      </div>

      {diagnosticOpen && diagnostic && (
        <div className="full-edge-preparation-diagnostic" aria-live="polite">
          <strong>{safeDiagnosticText(diagnostic.code, t('preparation.diagnosticUnavailable'))}</strong>
          <p>{safeDiagnosticText(diagnostic.summary, t('preparation.diagnosticUnavailable'))}</p>
          {diagnostic.checks?.map((check) => (
            <p key={`${check.name}:${check.status}`}>
              <code>{safeDiagnosticText(check.name, t('preparation.diagnosticUnavailable'))}</code>: {safeDiagnosticText(check.status, t('preparation.diagnosticUnavailable'))}{check.detail ? ` — ${safeDiagnosticText(check.detail, t('preparation.diagnosticUnavailable'))}` : ''}
            </p>
          ))}
        </div>
      )}
    </section>
  );
};
