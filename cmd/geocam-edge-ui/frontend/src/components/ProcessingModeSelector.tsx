import React from 'react';
import { ProcessingModeOption, ProcessingMode, DependencyStatus, VisionRuntimeStatus } from '../types/installer';
import { capabilityBadge } from '../utils/processingModeDisplay';
import { Button } from './Button';
import { HelpLink } from './HelpLink';
import { useI18n } from '../i18n/I18nContext';
import { docsLinks } from '../config/docsLinks';
import { useFFmpeg } from '../hooks/useFFmpeg';
import { useVisionRuntime } from '../hooks/useVisionRuntime';

interface ProcessingModeSelectorProps {
  options: ProcessingModeOption[];
  currentMode: ProcessingMode | null;
  selected: ProcessingMode | null;
  onSelect: (mode: ProcessingMode) => void;
  onContinue: () => void;
  loading: boolean;
  ffmpegStatus?: DependencyStatus | null;
  onInstallFFmpeg?: () => void;
  installingFFmpeg?: boolean;
  verifyingFFmpeg?: boolean;
  ffmpegError?: string | null;
  visionStatus?: VisionRuntimeStatus | null;
  onPrepareVision?: () => void;
  provisioningVision?: boolean;
  visionError?: string | null;
  onRefreshModes?: () => void;
}

export const ProcessingModeSelector: React.FC<ProcessingModeSelectorProps> = ({
  options,
  currentMode,
  selected,
  onSelect,
  onContinue,
  loading,
  ffmpegStatus,
  onInstallFFmpeg,
  installingFFmpeg,
  verifyingFFmpeg,
  ffmpegError,
  visionStatus,
  onPrepareVision,
  provisioningVision,
  visionError,
  onRefreshModes,
}) => {
  const { t } = useI18n();
  const hookFFmpeg = useFFmpeg();
  const hookVision = useVisionRuntime();

  const status = ffmpegStatus !== undefined ? ffmpegStatus : hookFFmpeg.status;
  const installing = installingFFmpeg !== undefined ? installingFFmpeg : hookFFmpeg.installing;
  const verifying = verifyingFFmpeg !== undefined ? verifyingFFmpeg : hookFFmpeg.verifying;
  const installError = ffmpegError !== undefined ? ffmpegError : hookFFmpeg.error;
  const handleInstall = onInstallFFmpeg || hookFFmpeg.install;

  const currentVision = visionStatus !== undefined ? visionStatus : hookVision.status;
  const provisioningV = provisioningVision !== undefined ? provisioningVision : hookVision.provisioning;
  const currentVisionError = visionError !== undefined ? visionError : hookVision.error;

  const handlePrepare = async () => {
    if (onPrepareVision) {
      onPrepareVision();
    } else {
      try {
        await hookVision.prepare();
        if (onRefreshModes) {
          onRefreshModes();
        }
      } catch {
        // error handled by hook
      }
    }
  };

  const handleRecheck = async () => {
    await hookVision.check();
    if (onRefreshModes) {
      onRefreshModes();
    }
  };

  const handleImportModel = async (modelName: string) => {
    try {
      await hookVision.importSingleModel(modelName);
      if (onRefreshModes) {
        onRefreshModes();
      }
    } catch {
      // error handled in hook
    }
  };

  const selectedOption = options.find((o) => o.mode === selected);
  const isSelectedUnavailable = selectedOption?.capability === 'UNAVAILABLE';
  const fullEdgeOption = options.find((o) => o.mode === 'full_edge');

  return (
    <div className="card mode-selector-card">
      <div className="card-header-row">
        <h3 className="card-title">{t('mode.chooseTitle')}</h3>
        <HelpLink href={docsLinks.processingModes}>{t('mode.whichShouldIChoose')}</HelpLink>
      </div>

      {status && !status.ready && (
        <div
          className="card"
          style={{
            border: '1px solid var(--danger)',
            backgroundColor: 'rgba(239, 68, 68, 0.08)',
            marginBottom: '1.25rem',
            padding: '1rem',
          }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '0.75rem' }}>
            <div>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <span className="check-icon fail" aria-hidden="true">✗</span>
                <strong style={{ color: 'var(--danger)' }}>{t('ffmpeg.notInstalled')}</strong>
              </div>
              <p style={{ margin: '0.25rem 0 0.5rem', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
                {t('ffmpeg.missingDesc')}
              </p>
              <p style={{ margin: 0, fontSize: '0.8rem', color: 'var(--text-muted)' }}>
                {t('ffmpeg.path')} <code>{status.ffmpeg_path || t('ffmpeg.pathNotFound')}</code>
              </p>
            </div>
            <div>
              <Button
                variant="primary"
                onClick={handleInstall}
                loading={installing || verifying}
                disabled={installing || verifying}
              >
                {installing ? t('ffmpeg.installing') : verifying ? t('ffmpeg.verifying') : t('ffmpeg.installAction')}
              </Button>
            </div>
          </div>
          {installError && (
            <div
              style={{
                marginTop: '0.75rem',
                padding: '0.5rem',
                backgroundColor: 'rgba(239, 68, 68, 0.15)',
                borderRadius: 'var(--radius-sm)',
                fontSize: '0.85rem',
                color: 'var(--danger)',
              }}
            >
              <p style={{ margin: 0 }}>{t('ffmpeg.installFailed', { error: installError })}</p>
              <p style={{ margin: '0.25rem 0 0', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                {t('ffmpeg.manualInstruction')}
              </p>
            </div>
          )}
        </div>
      )}

      {status && status.ready && (
        <div
          className="card"
          style={{
            border: '1px solid var(--success)',
            backgroundColor: 'rgba(16, 185, 129, 0.06)',
            marginBottom: '1.25rem',
            padding: '0.75rem 1rem',
          }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '0.5rem' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
              <span className="check-icon pass" aria-hidden="true">✓</span>
              <span style={{ color: 'var(--success)', fontWeight: 600 }}>{t('ffmpeg.ready')}</span>
            </div>
            <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
              <span>{t('ffmpeg.version')} {status.ffmpeg_version}</span>
              {' · '}
              <span><code>{status.ffmpeg_path}</code></span>
            </div>
          </div>
        </div>
      )}

      <div className="mode-card-grid">
        {options.map((option) => {
          const badge = capabilityBadge(option);
          const isUnavailable = option.capability === 'UNAVAILABLE';
          const isSelected = selected === option.mode;
          return (
            <button
              key={option.mode}
              type="button"
              className={`mode-card ${isSelected ? 'mode-card-selected' : ''} ${isUnavailable ? 'mode-card-disabled' : ''}`}
              onClick={() => onSelect(option.mode)}
              aria-pressed={isSelected}
            >
              <div className="mode-card-header">
                <span className="mode-card-name">{option.display_name}</span>
                {currentMode === option.mode && <span className="badge badge-info">{t('mode.current')}</span>}
              </div>
              <p className="mode-card-description">{option.description}</p>
              <dl className="mode-card-facts">
                <div>
                  <dt>{t('mode.localCompute')}</dt>
                  <dd>{option.local_compute}</dd>
                </div>
                <div>
                  <dt>{t('mode.networkDependency')}</dt>
                  <dd>{option.network_dependency}</dd>
                </div>
                <div>
                  <dt>{t('mode.inference')}</dt>
                  <dd>{option.inference_location}</dd>
                </div>
              </dl>
              <span className={`badge ${badge.className}`}>{badge.label}</span>
              {option.capability_reason && <p className="mode-card-reason">{option.capability_reason}</p>}
              {isUnavailable && option.blockers && option.blockers.length > 0 && (
                <p className="mode-card-reason">{t('mode.reason', { reason: option.blockers[0] })}</p>
              )}
              {option.capability === 'SUPPORTED_WITH_WARNINGS' && option.warnings && option.warnings.length > 0 && (
                <p className="mode-card-reason">{option.warnings[0]}</p>
              )}
            </button>
          );
        })}
      </div>

      {/* Full Edge Vision Runtime Provisioning / Status Section */}
      {(selected === 'full_edge' || (fullEdgeOption && fullEdgeOption.capability === 'UNAVAILABLE')) && currentVision && (
        <div
          className="card"
          style={{
            border: `1px solid ${
              currentVision.ready
                ? 'var(--success)'
                : currentVision.packages_ready
                ? 'var(--warning, #f59e0b)'
                : 'var(--danger)'
            }`,
            backgroundColor: currentVision.ready
              ? 'rgba(16, 185, 129, 0.06)'
              : currentVision.packages_ready
              ? 'rgba(245, 158, 11, 0.08)'
              : 'rgba(239, 68, 68, 0.08)',
            marginTop: '1.25rem',
            marginBottom: '1rem',
            padding: '1rem',
          }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '0.75rem' }}>
            <div style={{ flex: '1 1 300px' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <span className={`check-icon ${currentVision.ready ? 'pass' : 'fail'}`} aria-hidden="true">
                  {currentVision.ready ? '✓' : currentVision.packages_ready ? '⚠' : '✗'}
                </span>
                <strong
                  style={{
                    color: currentVision.ready
                      ? 'var(--success)'
                      : currentVision.packages_ready
                      ? 'var(--warning, #d97706)'
                      : 'var(--danger)',
                  }}
                >
                  {currentVision.ready
                    ? t('vision.ready', { device: currentVision.device_name })
                    : currentVision.packages_ready
                    ? t('vision.title')
                    : t('vision.notConfigured')}
                </strong>
              </div>
              {!currentVision.packages_ready && (
                <p style={{ margin: '0.25rem 0 0.5rem', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
                  {t('vision.missingDesc')}
                </p>
              )}
              {currentVision.python_found && (
                <p style={{ margin: '0.25rem 0 0', fontSize: '0.8rem', color: 'var(--text-muted)' }}>
                  {t('vision.pythonVersion', {
                    version: currentVision.python_version || 'N/A',
                    path: currentVision.python_path || 'N/A',
                  })}
                </p>
              )}
              {currentVision.packages_ready && !currentVision.ready && (
                <div style={{ marginTop: '0.5rem' }}>
                  <p style={{ margin: '0.25rem 0', fontSize: '0.85rem', color: 'var(--danger)', fontWeight: 500 }}>
                    {t('vision.missingModels', {
                      dir: currentVision.models_dir,
                      models: (currentVision.missing_models || []).join(', '),
                    })}
                  </p>
                  <p style={{ margin: '0 0 0.5rem', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                    {t('vision.missingModelsHelp')}
                  </p>
                  {currentVision.missing_models && currentVision.missing_models.length > 0 && (
                    <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                      {currentVision.missing_models.map((model) => (
                        <Button
                          key={model}
                          variant="secondary"
                          onClick={() => handleImportModel(model)}
                          loading={hookVision.loading}
                          disabled={hookVision.loading}
                        >
                          {t('vision.importModel', { model })}
                        </Button>
                      ))}
                    </div>
                  )}
                </div>
              )}
            </div>
            <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
              {!currentVision.packages_ready && (
                <Button
                  variant="primary"
                  onClick={handlePrepare}
                  loading={provisioningV}
                  disabled={provisioningV || !currentVision.can_provision}
                >
                  {provisioningV ? t('vision.preparing') : t('vision.prepareAction')}
                </Button>
              )}
              {currentVision.packages_ready && !currentVision.ready && (
                <Button
                  variant="secondary"
                  onClick={handleRecheck}
                  loading={hookVision.loading}
                  disabled={hookVision.loading}
                >
                  {t('vision.recheck')}
                </Button>
              )}
            </div>
          </div>
          {currentVisionError && (
            <div
              style={{
                marginTop: '0.75rem',
                padding: '0.75rem',
                backgroundColor: 'rgba(239, 68, 68, 0.15)',
                borderRadius: 'var(--radius-sm)',
                fontSize: '0.85rem',
                color: 'var(--danger)',
                display: 'flex',
                justifyContent: 'space-between',
                alignItems: 'center',
                flexWrap: 'wrap',
                gap: '0.5rem',
              }}
            >
              <p style={{ margin: 0, flex: '1 1 200px' }}>{t('vision.prepareFailed', { error: currentVisionError })}</p>
              <Button
                variant="secondary"
                onClick={handlePrepare}
                loading={provisioningV}
                disabled={provisioningV}
              >
                {t('vision.retry')}
              </Button>
            </div>
          )}
        </div>
      )}

      <div className="actions-bar" style={{ display: 'flex', flexDirection: 'column', alignItems: 'stretch', gap: '0.5rem' }}>
        {isSelectedUnavailable && (
          <p style={{ margin: 0, textAlign: 'center', color: 'var(--danger)', fontSize: '0.875rem' }}>
            {t('mode.cannotContinueUnavailable')}
          </p>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
          <Button
            variant="primary"
            onClick={onContinue}
            disabled={!selected || isSelectedUnavailable || loading}
            loading={loading}
          >
            {t('mode.continue')}
          </Button>
        </div>
      </div>
    </div>
  );
};
